package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"lmtools/internal/constants"
	"lmtools/internal/core"
	"lmtools/internal/retry"
	"lmtools/internal/session"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// newBranchingServer returns a server whose backend answers "answer N" to
// its Nth request, and a function that returns each request body sent so far.
func newBranchingServer(t *testing.T) (*Server, func() []string) {
	t.Helper()
	return newBranchingServerAt(t, t.TempDir())
}

// branchingBackendForTest runs before the backend of a branching server
// answers.
var branchingBackendForTest func()

// newBranchingServerAt is newBranchingServer keeping its state in dir.
func newBranchingServerAt(t *testing.T, dir string) (*Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var bodies []string
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		if branchingBackendForTest != nil {
			branchingBackendForTest()
		}
		mu.Lock()
		bodies = append(bodies, string(body))
		n := len(bodies)
		mu.Unlock()
		return jsonRoundTripResponse(http.StatusOK, AnthropicResponse{
			ID: "msg_backend", Type: "message", Role: "assistant", Model: "claude-test", StopReason: "end_turn",
			Content: []AnthropicContentBlock{{Type: "text", Text: fmt.Sprintf("answer %d", n)}},
			Usage:   &AnthropicUsage{InputTokens: 10, OutputTokens: 3},
		}), nil
	})
	config := &Config{
		Provider:           constants.ProviderAnthropic,
		ProviderURL:        "http://anthropic.local",
		ProviderKeySet:     ProviderKeySet{AnthropicAPIKey: "test-key"},
		MaxRequestBodySize: fixtureMaxBodySize,
		SessionsDir:        dir,
	}
	client := retry.NewClientWithTransport(10*time.Second, 0, &retryLoggerAdapter{ctx: context.Background()}, extractRequestLogger, transport)
	server := NewTestServerDirectWithClient(t, config, client)
	return server, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), bodies...)
	}
}

func responseID(t *testing.T, payload map[string]interface{}) string {
	t.Helper()
	id, _ := payload["id"].(string)
	if id == "" {
		t.Fatalf("response without an id: %#v", payload)
	}
	return id
}

func loadRecord(t *testing.T, server *Server, id string) *responseRecord {
	t.Helper()
	rec, ok, err := server.responsesState.loadResponse(id)
	if err != nil || !ok {
		t.Fatalf("response %s: ok %v: %v", id, ok, err)
	}
	return rec
}

func historyText(t *testing.T, server *Server, rec *responseRecord) string {
	t.Helper()
	history, err := server.responsesState.history(rec.Session, rec.MessageID, rec.HiddenItems)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Join(responseTypedTextLines(history), "\n")
}

// preparedRequest prepares a stored request with input and the extra fields
// of payload, and returns what its commit needs.
type preparedRequest struct {
	req   *OpenAIResponsesRequest
	typed TypedRequest
	state *openAIResponsesStateContext
}

func prepare(t *testing.T, server *Server, req *OpenAIResponsesRequest) preparedRequest {
	t.Helper()
	text, _ := req.Input.(string)
	typed := TypedRequest{Messages: []core.TypedMessage{core.NewTextMessage(string(core.RoleUser), text)}}
	state, _, err := server.prepareOpenAIResponsesStateForeground(context.Background(), req, nil, typed)
	if err != nil {
		t.Fatalf("prepare %q: %v", text, err)
	}
	return preparedRequest{req: req, typed: typed, state: state}
}

func (p preparedRequest) commit(t *testing.T, server *Server, id, answer string) error {
	t.Helper()
	return p.commitWithStatus(t, server, id, answer, "completed")
}

func (p preparedRequest) commitWithStatus(t *testing.T, server *Server, id, answer, status string) error {
	t.Helper()
	resp := &OpenAIResponsesResponse{
		ID: id, Object: "response", Status: status, Model: "claude-test", CreatedAt: time.Now().Unix(),
		Output: []OpenAIResponsesOutputItem{{
			Type: "message", Status: "completed", Role: core.RoleAssistant,
			Content: []OpenAIResponsesContentPart{{Type: "output_text", Text: answer}},
		}},
	}
	return server.commitOpenAIResponsesStateWithBlocks(context.Background(), p.state, p.req, p.typed, resp, "claude-test", nil)
}

// Continuing an older response branches the stored conversation in place:
// the new response shares the session, and the newer one keeps its history.
func TestContinuingAnOlderResponseBranchesTheSameSession(t *testing.T) {
	server, bodies := newBranchingServer(t)
	first := responseID(t, postResponses(t, server, map[string]interface{}{"model": "claude-test", "input": "first question"}))
	second := responseID(t, postResponses(t, server, map[string]interface{}{"model": "claude-test", "input": "second question", "previous_response_id": first}))
	branch := responseID(t, postResponses(t, server, map[string]interface{}{"model": "claude-test", "input": "branch question", "previous_response_id": first}))

	firstRec, secondRec, branchRec := loadRecord(t, server, first), loadRecord(t, server, second), loadRecord(t, server, branch)
	if branchRec.Session != firstRec.Session || secondRec.Session != firstRec.Session {
		t.Fatalf("sessions %s, %s, %s; want one", firstRec.Session, secondRec.Session, branchRec.Session)
	}
	if got := historyText(t, server, secondRec); got != "user:first question\nassistant:answer 1\nuser:second question\nassistant:answer 2" {
		t.Fatalf("second response history:\n%s", got)
	}
	if got := historyText(t, server, branchRec); got != "user:first question\nassistant:answer 1\nuser:branch question\nassistant:answer 3" {
		t.Fatalf("branch history:\n%s", got)
	}
	if sent := bodies()[2]; strings.Contains(sent, "second question") || !strings.Contains(sent, "first question") {
		t.Fatalf("branch request: %s", sent)
	}
	if n := storedMessageCount(t, server, firstRec.Session); n != 6 {
		t.Fatalf("session holds %d messages, want 6 with no copies", n)
	}
}

// Two requests that continue one response both save, each as its own branch.
func TestConcurrentContinuationsOfOneResponseBothSave(t *testing.T) {
	server, _ := newBranchingServer(t)
	first := responseID(t, postResponses(t, server, map[string]interface{}{"model": "claude-test", "input": "question"}))
	a := prepare(t, server, &OpenAIResponsesRequest{Model: "claude-test", Input: "follow-up a", PreviousResponseID: first})
	b := prepare(t, server, &OpenAIResponsesRequest{Model: "claude-test", Input: "follow-up b", PreviousResponseID: first})
	if err := a.commit(t, server, "resp_a", "answer a"); err != nil {
		t.Fatal(err)
	}
	if err := b.commit(t, server, "resp_b", "answer b"); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{"resp_a": "follow-up a\nassistant:answer a", "resp_b": "follow-up b\nassistant:answer b"} {
		if got := historyText(t, server, loadRecord(t, server, id)); !strings.HasSuffix(got, want) || !strings.HasPrefix(got, "user:question\nassistant:answer 1") {
			t.Fatalf("%s history:\n%s", id, got)
		}
	}
}

// Two requests that start from an empty conversation before either saves
// both have a place to append: the session root. The first advances the
// conversation, and the second is kept as a branch.
func TestConcurrentFirstRequestsOfAnEmptyConversationBothSave(t *testing.T) {
	server, _ := newBranchingServer(t)
	conv, err := server.responsesState.createConversation(context.Background(), nil, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	a := prepare(t, server, &OpenAIResponsesRequest{Model: "claude-test", Input: "first a", Conversation: conv.ID})
	b := prepare(t, server, &OpenAIResponsesRequest{Model: "claude-test", Input: "first b", Conversation: conv.ID})
	if err := a.commit(t, server, "resp_first_a", "answer a"); err != nil {
		t.Fatal(err)
	}
	if err := b.commit(t, server, "resp_first_b", "answer b"); err != nil {
		t.Fatal(err)
	}
	after, _, err := server.responsesState.loadConversation(conv.ID)
	if err != nil || after.LastResponseID != "resp_first_a" {
		t.Fatalf("conversation at %+v: %v", after, err)
	}
	if got := historyText(t, server, loadRecord(t, server, "resp_first_b")); got != "user:first b\nassistant:answer b" {
		t.Fatalf("second request's history:\n%s", got)
	}
}

// A change to a conversation while a request runs keeps the request from
// advancing it, although the conversation's last message is unchanged: the
// request saw the conversation as it was. The response is saved as a branch.
func TestConversationChangedDuringARequestDoesNotAdvance(t *testing.T) {
	for name, change := range map[string]func(*testing.T, *Server, string){
		"item deleted": func(t *testing.T, server *Server, id string) {
			requestJSON(t, server, http.MethodDelete, "/v1/conversations/"+id+"/items/item_0000", nil)
		},
		"instructions changed": func(t *testing.T, server *Server, id string) {
			requestJSON(t, server, http.MethodPost, "/v1/conversations/"+id, map[string]interface{}{"instructions": "be brief"})
		},
	} {
		t.Run(name, func(t *testing.T) {
			server, _ := newBranchingServer(t)
			created := requestJSON(t, server, http.MethodPost, "/v1/conversations", map[string]interface{}{"items": []interface{}{
				map[string]interface{}{"type": "message", "role": "user", "content": "opening note"},
			}})
			convID := responseID(t, created)
			before, _, err := server.responsesState.loadConversation(convID)
			if err != nil {
				t.Fatal(err)
			}
			p := prepare(t, server, &OpenAIResponsesRequest{Model: "claude-test", Input: "question", Conversation: convID})
			change(t, server, convID)
			if err := p.commit(t, server, "resp_during_change", "answer"); err != nil {
				t.Fatal(err)
			}
			after, _, err := server.responsesState.loadConversation(convID)
			if err != nil || after.Head != before.Head || after.LastResponseID != "" {
				t.Fatalf("conversation moved from %s to %s (last response %q): %v", before.Head, after.Head, after.LastResponseID, err)
			}
			if rec := loadRecord(t, server, "resp_during_change"); rec.MessageID == before.Head || rec.ConversationID != convID {
				t.Fatalf("response record %+v", rec)
			}
		})
	}
}

// Updating a conversation and deleting one of its items hold the state lock
// from reading the record to saving it, so a response that advances the
// conversation meanwhile is not undone by the save.
func TestConversationChangesHoldTheStateLock(t *testing.T) {
	server, _ := newBranchingServer(t)
	created := requestJSON(t, server, http.MethodPost, "/v1/conversations", map[string]interface{}{"items": []interface{}{
		map[string]interface{}{"type": "message", "role": "user", "content": "opening note"},
	}})
	convID := responseID(t, created)
	changes := 0
	conversationChangeForTest = func() {
		if server.responsesState.mu.TryLock() {
			server.responsesState.mu.Unlock()
			t.Error("a conversation change runs without the state lock")
		}
		changes++
	}
	t.Cleanup(func() { conversationChangeForTest = nil })
	requestJSON(t, server, http.MethodPost, "/v1/conversations/"+convID, map[string]interface{}{"instructions": "be brief"})
	requestJSON(t, server, http.MethodDelete, "/v1/conversations/"+convID+"/items/item_0000", nil)
	if changes != 2 {
		t.Fatalf("%d of 2 changes ran under the state lock", changes)
	}
	conv, _, err := server.responsesState.loadConversation(convID)
	if err != nil || conv.Instructions != "be brief" || len(conv.DeletedItems) != 1 {
		t.Fatalf("conversation %+v: %v", conv, err)
	}
}

// The response record is saved before the conversation advances, so a crash
// between the two leaves a readable response and the conversation where it
// was.
func TestResponseRecordIsSavedBeforeTheConversationAdvances(t *testing.T) {
	server, _ := newBranchingServer(t)
	conv, err := server.responsesState.createConversation(context.Background(), nil, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	p := prepare(t, server, &OpenAIResponsesRequest{Model: "claude-test", Input: "question", Conversation: conv.ID})
	beforeConversationAdvanceForTest = func() error { return errors.New("crash") }
	t.Cleanup(func() { beforeConversationAdvanceForTest = nil })
	if err := p.commit(t, server, "resp_crash", "answer"); err == nil {
		t.Fatal("the commit survived the crash")
	}
	if got := historyText(t, server, loadRecord(t, server, "resp_crash")); got != "user:question\nassistant:answer" {
		t.Fatalf("response history:\n%s", got)
	}
	after, _, err := server.responsesState.loadConversation(conv.ID)
	if err != nil || after.Head != "" || after.LastResponseID != "" {
		t.Fatalf("conversation %+v: %v", after, err)
	}
}

// A response keeps the item visibility it was prepared with, so continuing
// it later still leaves out an item the conversation had deleted.
func TestContinuingAResponseKeepsItsHiddenItems(t *testing.T) {
	server, bodies := newBranchingServer(t)
	created := requestJSON(t, server, http.MethodPost, "/v1/conversations", map[string]interface{}{"items": []interface{}{
		map[string]interface{}{"type": "message", "role": "user", "content": "keep this note"},
		map[string]interface{}{"type": "message", "role": "user", "content": "secret note"},
	}})
	convID := responseID(t, created)
	requestJSON(t, server, http.MethodDelete, "/v1/conversations/"+convID+"/items/item_0001", nil)
	inConversation := responseID(t, postResponses(t, server, map[string]interface{}{"model": "claude-test", "input": "question", "conversation": convID}))
	responseID(t, postResponses(t, server, map[string]interface{}{"model": "claude-test", "input": "follow-up", "previous_response_id": inConversation}))
	sent := bodies()
	for i, body := range sent {
		if strings.Contains(body, "secret note") || !strings.Contains(body, "keep this note") {
			t.Fatalf("request %d sent the deleted item or lost the kept one: %s", i+1, body)
		}
	}
}

// A request that names its conversation leaves out the items the
// conversation deleted, also when it continues a response made before the
// deletion, and the response it makes keeps them hidden for the requests
// that continue it.
func TestConversationDeletionsHideItemsFromContinuedResponses(t *testing.T) {
	server, bodies := newBranchingServer(t)
	created := requestJSON(t, server, http.MethodPost, "/v1/conversations", map[string]interface{}{"items": []interface{}{
		map[string]interface{}{"type": "message", "role": "user", "content": "keep this note"},
		map[string]interface{}{"type": "message", "role": "user", "content": "secret note"},
	}})
	convID := responseID(t, created)
	first := responseID(t, postResponses(t, server, map[string]interface{}{"model": "claude-test", "input": "q1", "conversation": convID}))
	requestJSON(t, server, http.MethodDelete, "/v1/conversations/"+convID+"/items/item_0001", nil)
	second := responseID(t, postResponses(t, server, map[string]interface{}{"model": "claude-test", "input": "q2", "conversation": convID, "previous_response_id": first}))
	responseID(t, postResponses(t, server, map[string]interface{}{"model": "claude-test", "input": "q3", "previous_response_id": second}))
	sent := bodies()
	for i, body := range sent[1:] {
		if strings.Contains(body, "secret note") || !strings.Contains(body, "keep this note") {
			t.Fatalf("request %d after the deletion sent the deleted item or lost the kept one: %s", i+2, body)
		}
	}
}

// A conversation advances only from its last response: a request that
// continues an earlier response leaves it, also when the last response
// added no message, so both end at the same message.
func TestConversationAdvancesOnlyFromItsLastResponse(t *testing.T) {
	server, _ := newBranchingServer(t)
	conv, err := server.responsesState.createConversation(context.Background(), nil, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := prepare(t, server, &OpenAIResponsesRequest{Model: "claude-test", Input: "q1", Conversation: conv.ID}).commit(t, server, "resp_first", "a1"); err != nil {
		t.Fatal(err)
	}
	emptyReq := &OpenAIResponsesRequest{Model: "claude-test", Input: []interface{}{}, Conversation: conv.ID}
	state, _, err := server.prepareOpenAIResponsesStateForeground(context.Background(), emptyReq, nil, TypedRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if err := (preparedRequest{req: emptyReq, state: state}).commit(t, server, "resp_empty", ""); err != nil {
		t.Fatal(err)
	}
	if err := prepare(t, server, &OpenAIResponsesRequest{Model: "claude-test", Input: "q3", Conversation: conv.ID, PreviousResponseID: "resp_first"}).commit(t, server, "resp_branch", "a3"); err != nil {
		t.Fatal(err)
	}
	after, _, err := server.responsesState.loadConversation(conv.ID)
	if err != nil || after.LastResponseID != "resp_empty" {
		t.Fatalf("conversation %+v: %v; want it at resp_empty", after, err)
	}
}

// Instructions a request inherits from the response it continues do not
// replace the conversation's own when the request advances it.
func TestInheritedInstructionsLeaveTheConversationsOwn(t *testing.T) {
	server, _ := newBranchingServer(t)
	conv, err := server.responsesState.createConversation(context.Background(), nil, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := prepare(t, server, &OpenAIResponsesRequest{Model: "claude-test", Input: "q1", Conversation: conv.ID, Instructions: "first instructions"}).commit(t, server, "resp_first", "a1"); err != nil {
		t.Fatal(err)
	}
	requestJSON(t, server, http.MethodPost, "/v1/conversations/"+conv.ID, map[string]interface{}{"instructions": "updated instructions"})
	if err := prepare(t, server, &OpenAIResponsesRequest{Model: "claude-test", Input: "q2", Conversation: conv.ID, PreviousResponseID: "resp_first"}).commit(t, server, "resp_second", "a2"); err != nil {
		t.Fatal(err)
	}
	after, _, err := server.responsesState.loadConversation(conv.ID)
	if err != nil || after.LastResponseID != "resp_second" || after.Instructions != "updated instructions" {
		t.Fatalf("conversation %+v: %v", after, err)
	}
}

// A reply that was cut short is saved marked incomplete.
func TestCutShortRepliesAreSavedIncomplete(t *testing.T) {
	server, _ := newBranchingServer(t)
	for _, status := range []string{"completed", "incomplete", "failed"} {
		if err := prepare(t, server, &OpenAIResponsesRequest{Model: "claude-test", Input: "q"}).commitWithStatus(t, server, "resp_"+status, "partial", status); err != nil {
			t.Fatal(err)
		}
		rec := loadRecord(t, server, "resp_"+status)
		m, err := server.responsesState.store.Message(session.Ref{Session: rec.Session, Message: rec.MessageID})
		if err != nil || m.Role != core.RoleAssistant || m.Incomplete != (status != "completed") {
			t.Fatalf("%s reply %+v: %v", status, m, err)
		}
	}
}

// A deleted item is named by the stored message it comes from, so the
// deletion holds on the branches that contain that message and hides
// nothing on another: continuing an earlier response with another question
// keeps that question in the requests that follow it.
func TestDeletedItemsHoldOnlyOnTheirBranch(t *testing.T) {
	server, bodies := newBranchingServer(t)
	convID := responseID(t, requestJSON(t, server, http.MethodPost, "/v1/conversations", map[string]interface{}{}))
	first := responseID(t, postResponses(t, server, map[string]interface{}{"model": "claude-test", "input": "first question", "conversation": convID}))
	postResponses(t, server, map[string]interface{}{"model": "claude-test", "input": "second question", "conversation": convID})
	requestJSON(t, server, http.MethodDelete, "/v1/conversations/"+convID+"/items/item_0002", nil)
	alternative := responseID(t, postResponses(t, server, map[string]interface{}{"model": "claude-test", "input": "alternative question", "conversation": convID, "previous_response_id": first}))
	postResponses(t, server, map[string]interface{}{"model": "claude-test", "input": "follow-up", "previous_response_id": alternative})
	sent := bodies()
	last := sent[len(sent)-1]
	for _, want := range []string{"first question", "alternative question", "answer 3", "follow-up"} {
		if !strings.Contains(last, want) {
			t.Fatalf("the follow-up request lacks %q: %s", want, last)
		}
	}
	listed := requestJSON(t, server, http.MethodGet, "/v1/conversations/"+convID+"/items", nil)
	if text := fmt.Sprint(listed["data"]); strings.Contains(text, "second question") || !strings.Contains(text, "first question") {
		t.Fatalf("the conversation's items after the deletion: %s", text)
	}
}

// appendWhileHeld makes the first writer of a conversation's record that
// reads it wait while server appends an item to conversation convID, and
// returns a channel that receives the append's status.
func appendWhileHeld(t *testing.T, server *Server, convID, text string) <-chan int {
	t.Helper()
	var started atomic.Bool
	appended := make(chan int, 1)
	conversationChangeForTest = func() {
		if !started.CompareAndSwap(false, true) {
			return
		}
		go func() {
			data, _ := json.Marshal(map[string]interface{}{"items": []interface{}{
				map[string]interface{}{"type": "message", "role": "user", "content": text},
			}})
			req := httptest.NewRequest(http.MethodPost, "/v1/conversations/"+convID+"/items", bytes.NewReader(data))
			req.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()
			server.ServeHTTP(recorder, req)
			appended <- recorder.Code
		}()
		time.Sleep(200 * time.Millisecond)
	}
	t.Cleanup(func() { conversationChangeForTest = nil })
	return appended
}

// Two apiproxy instances sharing a sessions directory each hold a
// conversation's lock from reading its record to saving it, so an item one
// appends while the other updates the conversation stays in it.
func TestConversationWritersInTwoInstancesKeepEachOthersChanges(t *testing.T) {
	dir := t.TempDir()
	first, _ := newBranchingServerAt(t, dir)
	second, _ := newBranchingServerAt(t, dir)
	convID := responseID(t, requestJSON(t, first, http.MethodPost, "/v1/conversations", map[string]interface{}{}))
	appended := appendWhileHeld(t, second, convID, "appended note")
	requestJSON(t, first, http.MethodPost, "/v1/conversations/"+convID, map[string]interface{}{"metadata": map[string]interface{}{"topic": "locks"}})
	if code := <-appended; code != http.StatusOK {
		t.Fatalf("the append through the second instance answered %d", code)
	}
	listed := requestJSON(t, first, http.MethodGet, "/v1/conversations/"+convID+"/items", nil)
	if text := fmt.Sprint(listed["data"]); !strings.Contains(text, "appended note") {
		t.Fatalf("the conversation lost the item the other instance appended: %s", text)
	}
	conv, _, err := first.responsesState.loadConversation(convID)
	if err != nil || conv.Metadata["topic"] != "locks" {
		t.Fatalf("conversation %+v: %v", conv, err)
	}
}

// A response that advances its conversation holds the conversation's lock
// from reading the record to saving it, so an item another instance appends
// meanwhile follows the response.
func TestConversationAdvanceInOneInstanceKeepsAnAppendInAnother(t *testing.T) {
	dir := t.TempDir()
	first, _ := newBranchingServerAt(t, dir)
	second, _ := newBranchingServerAt(t, dir)
	convID := responseID(t, requestJSON(t, first, http.MethodPost, "/v1/conversations", map[string]interface{}{}))
	postResponses(t, first, map[string]interface{}{"model": "claude-test", "input": "first question", "conversation": convID})
	appended := appendWhileHeld(t, second, convID, "appended note")
	postResponses(t, first, map[string]interface{}{"model": "claude-test", "input": "second question", "conversation": convID})
	if code := <-appended; code != http.StatusOK {
		t.Fatalf("the append through the second instance answered %d", code)
	}
	listed := requestJSON(t, first, http.MethodGet, "/v1/conversations/"+convID+"/items?order=asc", nil)
	text := fmt.Sprint(listed["data"])
	last := -1
	for _, want := range []string{"first question", "answer 1", "second question", "answer 2", "appended note"} {
		at := strings.Index(text, want)
		if at <= last {
			t.Fatalf("the conversation's items lack %q after the earlier ones: %s", want, text)
		}
		last = at
	}
}

// Deleting a conversation holds its lock from reading the record to saving
// it, so an append another instance makes meanwhile finds the conversation
// deleted instead of saving it back.
func TestConversationDeletionInOneInstanceHoldsAgainstAnAppendInAnother(t *testing.T) {
	dir := t.TempDir()
	first, _ := newBranchingServerAt(t, dir)
	second, _ := newBranchingServerAt(t, dir)
	convID := responseID(t, requestJSON(t, first, http.MethodPost, "/v1/conversations", map[string]interface{}{}))
	appended := appendWhileHeld(t, second, convID, "appended note")
	requestJSON(t, first, http.MethodDelete, "/v1/conversations/"+convID, nil)
	if code := <-appended; code == http.StatusOK {
		t.Fatal("the append to the deleted conversation succeeded")
	}
	if _, ok, err := first.responsesState.loadConversation(convID); ok || err != nil {
		t.Fatalf("the deleted conversation reads as present (%v)", err)
	}
}

// serve runs a request with a JSON body through server, as another apiproxy
// receives it, and returns its status. A test hook may call it from any
// goroutine.
func serve(server *Server, method, path string, body interface{}) int {
	var reader io.Reader = http.NoBody
	if body != nil {
		data, _ := json.Marshal(body)
		reader = bytes.NewReader(data)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, req)
	return recorder.Code
}

// statusOf returns the status of response id as server reads its record.
func statusOf(t *testing.T, server *Server, id string) string {
	t.Helper()
	rec, ok, err := server.responsesState.loadResponse(id)
	if err != nil || !ok {
		t.Fatalf("response %s: %t %v", id, ok, err)
	}
	return rec.Status
}

// A background response whose conversation another apiproxy deletes while
// the backend answers, or after the response's messages are saved, is not
// published: it ends failed.
func TestBackgroundResponseOfADeletedConversationEndsFailed(t *testing.T) {
	for _, moment := range []struct {
		name string
		hook *func()
	}{{"while the backend answers", &branchingBackendForTest}, {"after the messages are saved", &responseMessagesSavedForTest}} {
		t.Run(moment.name, func(t *testing.T) {
			dir := t.TempDir()
			first, _ := newBranchingServerAt(t, dir)
			second, _ := newBranchingServerAt(t, dir)
			convID := responseID(t, requestJSON(t, first, http.MethodPost, "/v1/conversations", map[string]interface{}{}))
			deleted := make(chan int, 1)
			*moment.hook = func() {
				deleted <- serve(second, http.MethodDelete, "/v1/conversations/"+convID, nil)
			}
			t.Cleanup(func() { *moment.hook = nil })
			respID := responseID(t, postResponses(t, first, map[string]interface{}{"model": "claude-test", "input": "q", "conversation": convID, "background": true}))
			if code := <-deleted; code != http.StatusOK {
				t.Fatalf("the deletion through the second instance answered %d", code)
			}
			waitForBackgroundIdle(t, first)
			if status := statusOf(t, first, respID); status != "failed" {
				t.Fatalf("the response of the deleted conversation is %s", status)
			}
		})
	}
}

// A background response cancelled through another apiproxy before its
// worker starts, or deleted there, is not sent to the backend.
func TestBackgroundResponseCancelledBeforeItsWorkerIsNotSent(t *testing.T) {
	for _, how := range []struct {
		name, method, suffix, want string
	}{{"cancelled", http.MethodPost, "/cancel", "cancelled"}, {"deleted", http.MethodDelete, "", ""}} {
		t.Run(how.name, func(t *testing.T) {
			dir := t.TempDir()
			first, bodies := newBranchingServerAt(t, dir)
			second, _ := newBranchingServerAt(t, dir)
			answered := make(chan int, 1)
			backgroundStartForTest = func(id string) {
				answered <- serve(second, how.method, "/v1/responses/"+id+how.suffix, nil)
			}
			t.Cleanup(func() { backgroundStartForTest = nil })
			respID := responseID(t, postResponses(t, first, map[string]interface{}{"model": "claude-test", "input": "q", "background": true}))
			if code := <-answered; code != http.StatusOK {
				t.Fatalf("the second instance answered %d", code)
			}
			waitForBackgroundIdle(t, first)
			if sent := bodies(); len(sent) != 0 {
				t.Fatalf("the backend got %d requests", len(sent))
			}
			rec, ok, err := first.responsesState.loadResponse(respID)
			if err != nil || !ok || rec.Status != how.want && !rec.Deleted {
				t.Fatalf("the response is %+v (%t %v)", rec, ok, err)
			}
		})
	}
}

// A background response that another apiproxy cancels after the response's
// messages are saved stays cancelled.
func TestBackgroundResponseCancelledInAnotherInstanceStaysCancelled(t *testing.T) {
	dir := t.TempDir()
	first, _ := newBranchingServerAt(t, dir)
	second, _ := newBranchingServerAt(t, dir)
	var respID atomic.Value
	cancelled := make(chan int, 1)
	ready := make(chan struct{})
	responseMessagesSavedForTest = func() {
		<-ready
		cancelled <- serve(second, http.MethodPost, "/v1/responses/"+respID.Load().(string)+"/cancel", nil)
	}
	t.Cleanup(func() { responseMessagesSavedForTest = nil })
	respID.Store(responseID(t, postResponses(t, first, map[string]interface{}{"model": "claude-test", "input": "q", "background": true})))
	close(ready)
	if code := <-cancelled; code != http.StatusOK {
		t.Fatalf("the cancellation through the second instance answered %d", code)
	}
	waitForBackgroundIdle(t, first)
	id := respID.Load().(string)
	if status := statusOf(t, first, id); status != "cancelled" {
		t.Fatalf("the cancelled response is %s", status)
	}
}

// A background response cancelled in its own apiproxy once its messages
// are saved ends cancelled.
func TestBackgroundResponseCancelledDuringItsCommitEndsCancelled(t *testing.T) {
	server, _ := newBranchingServer(t)
	var respID atomic.Value
	ready := make(chan struct{})
	responseMessagesSavedForTest = func() {
		<-ready
		server.cancelBackgroundResponse(respID.Load().(string))
	}
	t.Cleanup(func() { responseMessagesSavedForTest = nil })
	respID.Store(responseID(t, postResponses(t, server, map[string]interface{}{"model": "claude-test", "input": "q", "background": true})))
	close(ready)
	waitForBackgroundIdle(t, server)
	if status := statusOf(t, server, respID.Load().(string)); status != "cancelled" {
		t.Fatalf("the response cancelled during its commit is %s", status)
	}
}

// A conversation deletion in another apiproxy that comes while a response
// is published waits for the publication: the response is completed, and
// then the conversation is deleted.
func TestConversationDeletionWaitsForAPublication(t *testing.T) {
	dir := t.TempDir()
	first, _ := newBranchingServerAt(t, dir)
	second, _ := newBranchingServerAt(t, dir)
	convID := responseID(t, requestJSON(t, first, http.MethodPost, "/v1/conversations", map[string]interface{}{}))
	var started atomic.Bool
	deleted := make(chan int, 1)
	early := false
	conversationChangeForTest = func() {
		if !started.CompareAndSwap(false, true) {
			return
		}
		go func() { deleted <- serve(second, http.MethodDelete, "/v1/conversations/"+convID, nil) }()
		time.Sleep(200 * time.Millisecond)
		early = len(deleted) > 0
	}
	t.Cleanup(func() { conversationChangeForTest = nil })
	respID := responseID(t, postResponses(t, first, map[string]interface{}{"model": "claude-test", "input": "q", "conversation": convID}))
	if code := <-deleted; early || code != http.StatusOK {
		t.Fatalf("the deletion answered %d, before the publication ended: %t", code, early)
	}
	if status := statusOf(t, first, respID); status != "completed" {
		t.Fatalf("the published response is %s", status)
	}
	if _, ok, err := first.responsesState.loadConversation(convID); ok || err != nil {
		t.Fatalf("the conversation remains (%v)", err)
	}
}

// A cancellation in another apiproxy that comes while a background
// response is published waits for the publication and answers with the
// completed response.
func TestCancellationWaitsForAPublication(t *testing.T) {
	dir := t.TempDir()
	first, _ := newBranchingServerAt(t, dir)
	second, _ := newBranchingServerAt(t, dir)
	var respID atomic.Value
	var armed, started atomic.Bool
	ready := make(chan struct{})
	cancelled := make(chan *httptest.ResponseRecorder, 1)
	early := make(chan bool, 1)
	responseMessagesSavedForTest = func() {
		<-ready
		armed.Store(true)
	}
	responseChangeForTest = func() {
		if !armed.Load() || !started.CompareAndSwap(false, true) {
			return
		}
		go func() {
			req := httptest.NewRequest(http.MethodPost, "/v1/responses/"+respID.Load().(string)+"/cancel", http.NoBody)
			recorder := httptest.NewRecorder()
			second.ServeHTTP(recorder, req)
			cancelled <- recorder
		}()
		time.Sleep(200 * time.Millisecond)
		early <- len(cancelled) > 0
	}
	t.Cleanup(func() { responseMessagesSavedForTest, responseChangeForTest = nil, nil })
	respID.Store(responseID(t, postResponses(t, first, map[string]interface{}{"model": "claude-test", "input": "q", "background": true})))
	close(ready)
	if <-early {
		t.Fatal("the cancellation ended before the publication")
	}
	answer := <-cancelled
	waitForBackgroundIdle(t, first)
	id := respID.Load().(string)
	if status := statusOf(t, first, id); status != "completed" || !strings.Contains(answer.Body.String(), `"status":"completed"`) {
		t.Fatalf("the response is %s, and the cancellation answered %d %s", status, answer.Code, answer.Body.String())
	}
}

// A conversation whose record cannot be saved leaves no session behind with
// the items it was created with, however often the request is retried.
func TestFailedConversationCreationLeavesNoSession(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes in a directory without write permission")
	}
	server, _ := newBranchingServer(t)
	if err := server.responsesState.ensure(); err != nil {
		t.Fatal(err)
	}
	conversations := server.responsesState.conversationsDir
	if err := os.Chmod(conversations, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(conversations, 0o700) })
	body := map[string]interface{}{"items": []interface{}{
		map[string]interface{}{"type": "message", "role": "user", "content": "the first item"},
	}}
	for i := 0; i < 2; i++ {
		if code := serve(server, http.MethodPost, "/v1/conversations", body); code != http.StatusInternalServerError {
			t.Fatalf("creation %d answered %d", i+1, code)
		}
	}
	sessions, err := server.responsesState.store.Sessions()
	if err != nil || len(sessions) != 0 {
		t.Fatalf("sessions left behind: %+v (%v)", sessions, err)
	}
}

// A DELETE of a response that only the provider has reaches the provider
// without a lock file here, also when the responses directory is read-only.
func TestDeletingAProviderResponseTakesNoLocalLock(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes in a directory without write permission")
	}
	var mu sync.Mutex
	var calls []string
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		mu.Lock()
		calls = append(calls, r.Method+" "+r.URL.Path)
		mu.Unlock()
		return jsonRoundTripResponse(http.StatusOK, map[string]interface{}{"id": "resp_upstream", "object": "response.deleted", "deleted": true}), nil
	})
	config := &Config{
		Provider:           constants.ProviderOpenAI,
		ProviderURL:        "http://openai.local/v1",
		ProviderKeySet:     ProviderKeySet{OpenAIAPIKey: "test-key"},
		MaxRequestBodySize: fixtureMaxBodySize,
		SessionsDir:        t.TempDir(),
	}
	client := retry.NewClientWithTransport(10*time.Second, 0, &retryLoggerAdapter{ctx: context.Background()}, extractRequestLogger, transport)
	server := NewTestServerDirectWithClient(t, config, client)
	if err := server.responsesState.ensure(); err != nil {
		t.Fatal(err)
	}
	responses := server.responsesState.responsesDir
	if err := os.Chmod(responses, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(responses, 0o700) })
	if code := serve(server, http.MethodDelete, "/v1/responses/resp_upstream", nil); code != http.StatusOK {
		t.Fatalf("the DELETE answered %d", code)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 1 || calls[0] != "DELETE /v1/responses/resp_upstream" {
		t.Fatalf("provider calls %v", calls)
	}
}

// Lookups of a response only the provider has create no directory, so they
// reach the provider from a state root they cannot write, with conversations/
// not made yet.
func TestLookupsReachTheProviderFromAReadOnlyRoot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes in a directory without write permission")
	}
	var mu sync.Mutex
	var calls []string
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		mu.Lock()
		calls = append(calls, r.Method+" "+r.URL.Path)
		mu.Unlock()
		if r.Method == http.MethodDelete {
			return jsonRoundTripResponse(http.StatusOK, map[string]interface{}{"id": "resp_upstream", "object": "response.deleted", "deleted": true}), nil
		}
		return jsonRoundTripResponse(http.StatusOK, map[string]interface{}{"id": "resp_upstream", "object": "response", "status": "completed", "model": "gpt-upstream", "output": []interface{}{}}), nil
	})
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "responses"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(root, 0o700) })
	config := &Config{
		Provider:           constants.ProviderOpenAI,
		ProviderURL:        "http://openai.local/v1",
		ProviderKeySet:     ProviderKeySet{OpenAIAPIKey: "test-key"},
		MaxRequestBodySize: fixtureMaxBodySize,
		SessionsDir:        root,
	}
	client := retry.NewClientWithTransport(10*time.Second, 0, &retryLoggerAdapter{ctx: context.Background()}, extractRequestLogger, transport)
	server := NewTestServerDirectWithClient(t, config, client)
	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		if code := serve(server, method, "/v1/responses/resp_upstream", nil); code != http.StatusOK {
			t.Errorf("%s answered %d", method, code)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(calls, ", ") != "GET /v1/responses/resp_upstream, DELETE /v1/responses/resp_upstream" {
		t.Fatalf("provider calls %v", calls)
	}
}

// A writer of a stored response reads its record whole once, under its
// lock: the look before the lock reads the version alone. A background
// response's commit reads it once before its messages are saved and once
// as it is published.
func TestResponseWritersReadTheirRecordOnce(t *testing.T) {
	server, _ := newBranchingServer(t)
	ctx := context.Background()
	state := server.responsesState
	rec := &responseRecord{ID: "resp_counted", Object: "response", Status: "queued", Model: "claude-test", CreatedAt: 1}
	if err := state.saveResponse(ctx, rec); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	reads := map[string]int{}
	readRecordForTest = func(path string) {
		mu.Lock()
		reads[filepath.Base(path)]++
		mu.Unlock()
	}
	t.Cleanup(func() { readRecordForTest = nil })
	counted := func(name string, write func() error) {
		t.Helper()
		mu.Lock()
		reads = map[string]int{}
		mu.Unlock()
		if err := write(); err != nil {
			t.Fatal(name, err)
		}
		mu.Lock()
		defer mu.Unlock()
		if n := reads["resp_counted.json"]; n != 1 {
			t.Errorf("%s read the record whole %d times", name, n)
		}
	}
	counted("a status update", func() error { return state.updateResponseStatusIfPending(ctx, rec.ID, "in_progress", nil) })
	counted("a cancellation", func() error {
		_, _, err := state.cancelResponseIfPending(ctx, rec.ID, nil)
		return err
	})
	counted("a deletion", func() error {
		_, _, err := state.deleteResponse(ctx, rec.ID)
		return err
	})

	var armed atomic.Bool
	var published atomic.Int32
	branchingBackendForTest = func() { armed.Store(true) }
	readRecordForTest = func(path string) {
		if armed.Load() && strings.HasPrefix(filepath.Base(path), "resp_") {
			published.Add(1)
		}
	}
	t.Cleanup(func() { branchingBackendForTest = nil })
	postResponses(t, server, map[string]interface{}{"model": "claude-test", "input": "q", "background": true})
	waitForBackgroundIdle(t, server)
	if n := published.Load(); n != 2 {
		t.Fatalf("the commit of a background response read its record whole %d times", n)
	}
}

// GET and DELETE agree on which responses are stored here: a record whose
// version the fast look cannot read, written with the key in capitals, with
// an earlier version before the current one, or cut short, is read whole
// before a DELETE is forwarded, and none is.
func TestGetAndDeleteAgreeOnStoredResponses(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		mu.Lock()
		calls = append(calls, r.Method+" "+r.URL.Path)
		mu.Unlock()
		return jsonRoundTripResponse(http.StatusOK, map[string]interface{}{"id": "resp_upstream", "object": "response.deleted", "deleted": true}), nil
	})
	config := &Config{
		Provider:           constants.ProviderOpenAI,
		ProviderURL:        "http://openai.local/v1",
		ProviderKeySet:     ProviderKeySet{OpenAIAPIKey: "test-key"},
		MaxRequestBodySize: fixtureMaxBodySize,
		SessionsDir:        t.TempDir(),
	}
	client := retry.NewClientWithTransport(10*time.Second, 0, &retryLoggerAdapter{ctx: context.Background()}, extractRequestLogger, transport)
	server := NewTestServerDirectWithClient(t, config, client)
	if err := server.responsesState.ensure(); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		id, record string
		want       int
	}{
		{"resp_capitals", `{"Version":2,"id":"resp_capitals","object":"response","status":"completed"}`, http.StatusOK},
		{"resp_twice", `{"version":1,"id":"resp_twice","object":"response","status":"completed","version":2}`, http.StatusOK},
		{"resp_cut", `{`, http.StatusBadRequest},
	} {
		if err := os.WriteFile(server.responsesState.responsePath(c.id), []byte(c.record), 0o600); err != nil {
			t.Fatal(err)
		}
		for _, method := range []string{http.MethodGet, http.MethodDelete} {
			if code := serve(server, method, "/v1/responses/"+c.id, nil); code != c.want {
				t.Errorf("%s %s answered %d, want %d", method, c.id, code, c.want)
			}
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 0 {
		t.Fatalf("provider calls %v", calls)
	}
}

// newPassthroughServer returns a server that forwards to a provider what it
// does not store, and a function that returns the calls the provider got,
// each its method and its path as escaped on the wire.
func newPassthroughServer(t *testing.T) (*Server, func() []string) {
	t.Helper()
	return newPassthroughServerAt(t, t.TempDir())
}

// newPassthroughServerAt is newPassthroughServer keeping its state in dir.
func newPassthroughServerAt(t *testing.T, dir string) (*Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var calls []string
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		mu.Lock()
		calls = append(calls, r.Method+" "+r.URL.EscapedPath())
		mu.Unlock()
		if r.Method == http.MethodDelete {
			return jsonRoundTripResponse(http.StatusOK, map[string]interface{}{"id": "resp_upstream", "object": "response.deleted", "deleted": true}), nil
		}
		return jsonRoundTripResponse(http.StatusOK, map[string]interface{}{"id": "resp_upstream", "object": "response", "status": "completed", "model": "gpt-upstream", "output": []interface{}{}}), nil
	})
	config := &Config{
		Provider:           constants.ProviderOpenAI,
		ProviderURL:        "http://openai.local/v1",
		ProviderKeySet:     ProviderKeySet{OpenAIAPIKey: "test-key"},
		MaxRequestBodySize: fixtureMaxBodySize,
		SessionsDir:        dir,
	}
	client := retry.NewClientWithTransport(10*time.Second, 0, &retryLoggerAdapter{ctx: context.Background()}, extractRequestLogger, transport)
	server := NewTestServerDirectWithClient(t, config, client)
	if err := server.responsesState.ensure(); err != nil {
		t.Fatal(err)
	}
	return server, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), calls...)
	}
}

// A file named for one response that holds another's record is no record
// of the first: cancelling or deleting the first changes neither, and the
// second stays deleted.
func TestAFileHoldingAnotherResponseIsNotItsRecord(t *testing.T) {
	server, _ := newBranchingServer(t)
	ctx := context.Background()
	state := server.responsesState
	queued := &responseRecord{ID: "resp_b", Object: "response", Status: "queued", Model: "claude-test", CreatedAt: 1, Background: true}
	if err := state.saveResponse(ctx, queued); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(state.responsePath("resp_b"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(state.responsePath("resp_a"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(state.responsePath("resp_a")+".lock", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := state.deleteResponse(ctx, "resp_b"); err != nil || !ok {
		t.Fatalf("deleting resp_b: %t %v", ok, err)
	}
	for _, req := range []struct{ method, path string }{
		{http.MethodPost, "/v1/responses/resp_a/cancel"},
		{http.MethodDelete, "/v1/responses/resp_a"},
		{http.MethodGet, "/v1/responses/resp_a"},
	} {
		if code := serve(server, req.method, req.path, nil); code != http.StatusNotFound {
			t.Errorf("%s %s answered %d", req.method, req.path, code)
		}
	}
	if rec, ok, err := state.readResponse("resp_b"); err != nil || !ok || !rec.Deleted || rec.Status != "queued" {
		t.Fatalf("resp_b is %+v (%t %v)", rec, ok, err)
	}
}

// A file named for one conversation that holds another's record is no
// record of the first: updating or deleting the first changes neither, and
// the second stays deleted.
func TestAFileHoldingAnotherConversationIsNotItsRecord(t *testing.T) {
	server, _ := newBranchingServer(t)
	state := server.responsesState
	convB := responseID(t, requestJSON(t, server, http.MethodPost, "/v1/conversations", map[string]interface{}{}))
	data, err := os.ReadFile(state.conversationPath(convB))
	if err != nil {
		t.Fatal(err)
	}
	const convA = "conv_a"
	if err := os.WriteFile(state.conversationPath(convA), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(state.conversationPath(convA)+".lock", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	requestJSON(t, server, http.MethodDelete, "/v1/conversations/"+convB, nil)
	for _, req := range []struct {
		method, path string
		body         interface{}
	}{
		{http.MethodPost, "/v1/conversations/" + convA, map[string]interface{}{"metadata": map[string]interface{}{"topic": "x"}}},
		{http.MethodDelete, "/v1/conversations/" + convA, nil},
		{http.MethodGet, "/v1/conversations/" + convA, nil},
	} {
		if code := serve(server, req.method, req.path, req.body); code != http.StatusNotFound {
			t.Errorf("%s %s answered %d", req.method, req.path, code)
		}
	}
	if rec, ok, err := state.readConversation(convB); err != nil || !ok || !rec.Deleted {
		t.Fatalf("%s is %+v (%t %v)", convB, rec, ok, err)
	}
}

// A record whose first field is the current version but whose full reading
// is not, as an earlier version after it leaves it, belongs to the provider
// for DELETE as for GET: without its lock file, no lock file is made for
// it, also in a responses directory that cannot be written; with one, the
// lock is taken and the record found missing. A lock that cannot be taken
// for it does not keep the request here either.
func TestARecordKnownByItsFirstFieldAloneIsConfirmed(t *testing.T) {
	const record = `{"version":2,"id":"resp_rev","object":"response","status":"completed","version":1}`
	for _, c := range []struct {
		name               string
		lockFile, readOnly bool
		lockMode           os.FileMode
	}{
		{name: "without a lock file"},
		{name: "without a lock file, read-only", readOnly: true},
		{name: "with a lock file", lockFile: true, lockMode: 0o600},
		{name: "with a lock file it cannot open", lockFile: true, lockMode: 0o400},
	} {
		t.Run(c.name, func(t *testing.T) {
			if (c.readOnly || c.lockMode == 0o400) && os.Geteuid() == 0 {
				t.Skip("root writes in files and directories without write permission")
			}
			server, calls := newPassthroughServer(t)
			dir := server.responsesState.responsesDir
			path := server.responsesState.responsePath("resp_rev")
			if err := os.WriteFile(path, []byte(record), 0o600); err != nil {
				t.Fatal(err)
			}
			if c.lockFile {
				if err := os.WriteFile(path+".lock", nil, c.lockMode); err != nil {
					t.Fatal(err)
				}
			}
			if c.readOnly {
				if err := os.Chmod(dir, 0o500); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
			}
			for _, method := range []string{http.MethodGet, http.MethodDelete} {
				if code := serve(server, method, "/v1/responses/resp_rev", nil); code != http.StatusOK {
					t.Errorf("%s answered %d", method, code)
				}
			}
			if got := strings.Join(calls(), ", "); got != "GET /v1/responses/resp_rev, DELETE /v1/responses/resp_rev" {
				t.Errorf("provider calls %s", got)
			}
			if _, err := os.Stat(path + ".lock"); c.lockFile == os.IsNotExist(err) {
				t.Errorf("the lock file is there: %t", err == nil)
			}
		})
	}
}

// A response deleted here is not found by GET or by DELETE, and both go to
// the provider.
func TestADeletedResponseGoesToTheProvider(t *testing.T) {
	server, calls := newPassthroughServer(t)
	rec := &responseRecord{ID: "resp_gone", Object: "response", Status: "completed", Model: "gpt-upstream", CreatedAt: 1}
	if err := server.responsesState.saveResponse(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if code := serve(server, http.MethodDelete, "/v1/responses/resp_gone", nil); code != http.StatusOK {
			t.Fatalf("DELETE %d answered %d", i+1, code)
		}
	}
	if code := serve(server, http.MethodGet, "/v1/responses/resp_gone", nil); code != http.StatusOK {
		t.Fatalf("GET answered %d", code)
	}
	if got := strings.Join(calls(), ", "); got != "DELETE /v1/responses/resp_gone, GET /v1/responses/resp_gone" {
		t.Fatalf("provider calls %s", got)
	}
}

// newServerAnsweringAs returns a converted server whose backend gives its
// Nth reply the upstream ID ids[N], which the response takes as its own,
// and the text "the answer of" and that ID.
func newServerAnsweringAs(t *testing.T, ids ...string) *Server {
	t.Helper()
	var mu sync.Mutex
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		mu.Lock()
		defer mu.Unlock()
		if len(ids) == 0 {
			return nil, fmt.Errorf("the backend has no reply left")
		}
		id := ids[0]
		ids = ids[1:]
		return jsonRoundTripResponse(http.StatusOK, AnthropicResponse{
			ID: id, Type: "message", Role: "assistant", Model: "claude-test", StopReason: "end_turn",
			Content: []AnthropicContentBlock{{Type: "text", Text: "the answer of " + id}},
			Usage:   &AnthropicUsage{InputTokens: 1, OutputTokens: 1},
		}), nil
	})
	config := &Config{
		Provider:           constants.ProviderAnthropic,
		ProviderURL:        "http://anthropic.local",
		ProviderKeySet:     ProviderKeySet{AnthropicAPIKey: "test-key"},
		MaxRequestBodySize: fixtureMaxBodySize,
		SessionsDir:        t.TempDir(),
	}
	client := retry.NewClientWithTransport(10*time.Second, 0, &retryLoggerAdapter{ctx: context.Background()}, extractRequestLogger, transport)
	return NewTestServerDirectWithClient(t, config, client)
}

// IDs that differ only by case, or by the form of their Unicode, name files
// that differ also on a file system that folds case: two responses whose
// upstream IDs differ by case each keep their own answer.
func TestResponsesWhoseIDsDifferByCaseKeepTheirRecords(t *testing.T) {
	state := &responsesState{responsesDir: "/r", conversationsDir: "/c"}
	for _, pair := range [][2]string{{"resp_Case", "resp_case"}, {"resp_\u00e9", "resp_e\u0301"}, {"resp_%41", "resp_A"}} {
		a, b := filepath.Base(state.responsePath(pair[0])), filepath.Base(state.responsePath(pair[1]))
		if strings.EqualFold(a, b) || strings.ToLower(a) != a || strings.ToLower(b) != b {
			t.Errorf("%q and %q are written as %q and %q", pair[0], pair[1], a, b)
		}
	}
	if name := filepath.Base(state.responsePath("resp_0a1b-2c.d")); name != "resp_0a1b-2c.d.json" {
		t.Errorf("a plain ID is written as %q", name)
	}

	server := newServerAnsweringAs(t, "resp_Case", "resp_case")
	for _, want := range []string{"resp_Case", "resp_case"} {
		if got := responseID(t, postResponses(t, server, map[string]interface{}{"model": "claude-test", "input": "q"})); got != want {
			t.Fatalf("the response is %s, want %s", got, want)
		}
	}
	for _, id := range []string{"resp_Case", "resp_case"} {
		got := requestJSON(t, server, http.MethodGet, "/v1/responses/"+id, nil)
		if got["id"] != id || !strings.Contains(fmt.Sprint(got["output"]), "the answer of "+id) {
			t.Errorf("GET %s answered %v", id, got)
		}
	}
}

// A record is not written over a file that holds another response's
// record: publishing the response named for that file fails, and so does
// creating it, and the file keeps the other record.
func TestAFileHoldingAnotherRecordIsNotWrittenOver(t *testing.T) {
	server := newServerAnsweringAs(t, "resp_taken")
	state := server.responsesState
	if err := state.ensure(); err != nil {
		t.Fatal(err)
	}
	other, err := json.Marshal(responseRecord{Version: responsesStateVersion, ID: "resp_other", Object: "response", Status: "completed"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(state.responsePath("resp_taken"), other, 0o600); err != nil {
		t.Fatal(err)
	}
	if code := serve(server, http.MethodPost, "/v1/responses", map[string]interface{}{"model": "claude-test", "input": "q"}); code != http.StatusInternalServerError {
		t.Errorf("publishing over another record answered %d", code)
	}
	if err := state.saveResponse(context.Background(), &responseRecord{ID: "resp_taken", Object: "response", Status: "queued"}); !errors.Is(err, errResponsesStateOccupied) {
		t.Errorf("creating over another record: %v", err)
	}
	if rec, ok, err := state.readResponseFile("resp_taken"); err != nil || !ok || rec.ID != "resp_other" {
		t.Fatalf("the file holds %+v (%t %v)", rec, ok, err)
	}
}

// DELETE, GET and cancel of a response deleted here all reach the provider,
// also when the response's lock file cannot be opened, or is missing in a
// directory that cannot be written.
func TestADeletedResponseGoesToTheProviderWithoutItsLock(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes in files and directories without write permission")
	}
	for _, how := range []string{"a lock file it cannot open", "no lock file in a read-only directory"} {
		t.Run(how, func(t *testing.T) {
			server, calls := newPassthroughServer(t)
			state := server.responsesState
			rec := &responseRecord{ID: "resp_gone", Object: "response", Status: "completed", Model: "gpt-upstream", CreatedAt: 1}
			if err := state.saveResponse(context.Background(), rec); err != nil {
				t.Fatal(err)
			}
			if _, ok, err := state.deleteResponse(context.Background(), "resp_gone"); err != nil || !ok {
				t.Fatal(ok, err)
			}
			lock := state.responsePath("resp_gone") + ".lock"
			if how == "a lock file it cannot open" {
				if err := os.Chmod(lock, 0o400); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Remove(lock); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(state.responsesDir, 0o500); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(state.responsesDir, 0o700) })
			}
			for _, req := range []struct{ method, path string }{
				{http.MethodDelete, "/v1/responses/resp_gone"},
				{http.MethodGet, "/v1/responses/resp_gone"},
				{http.MethodPost, "/v1/responses/resp_gone/cancel"},
			} {
				if code := serve(server, req.method, req.path, nil); code != http.StatusOK {
					t.Errorf("%s %s answered %d", req.method, req.path, code)
				}
			}
			if got := strings.Join(calls(), ", "); got != "DELETE /v1/responses/resp_gone, GET /v1/responses/resp_gone, POST /v1/responses/resp_gone/cancel" {
				t.Errorf("provider calls %s", got)
			}
		})
	}
}

// A cancellation whose read under the response's lock finds the response
// deleted by another apiproxy goes to the provider, as for a response not
// stored here, and does not answer with the deleted record.
func TestCancellingAResponseDeletedMeanwhileGoesToTheProvider(t *testing.T) {
	dir := t.TempDir()
	first, calls := newPassthroughServerAt(t, dir)
	second, _ := newPassthroughServerAt(t, dir)
	rec := &responseRecord{ID: "resp_queued", Object: "response", Status: "queued", Model: "gpt-upstream", CreatedAt: 1, Background: true}
	if err := first.responsesState.saveResponse(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	deleted := make(chan int, 1)
	cancelForTest = func(id string) {
		deleted <- serve(second, http.MethodDelete, "/v1/responses/"+id, nil)
	}
	t.Cleanup(func() { cancelForTest = nil })
	req := httptest.NewRequest(http.MethodPost, "/v1/responses/resp_queued/cancel", http.NoBody)
	recorder := httptest.NewRecorder()
	first.ServeHTTP(recorder, req)
	if code := <-deleted; code != http.StatusOK {
		t.Fatalf("the deletion through the second instance answered %d", code)
	}
	if strings.Contains(recorder.Body.String(), `"queued"`) {
		t.Errorf("the cancellation answered with the deleted record: %s", recorder.Body.String())
	}
	if got := strings.Join(calls(), ", "); got != "POST /v1/responses/resp_queued/cancel" {
		t.Errorf("provider calls %s", got)
	}
}

// A request forwarded to the provider names what the client named: a
// repeated DELETE of response resp_%41, deleted here, reaches the provider
// for resp_%41, an item ID holding an escaped '/' reaches it whole, and a
// forwarded path is the client's path as the client escaped it, with the
// client's query after the resolved endpoint's query.
func TestForwardedRequestsNameWhatTheClientNamed(t *testing.T) {
	server, calls := newPassthroughServer(t)
	rec := &responseRecord{ID: "resp_%41", Object: "response", Status: "completed", Model: "gpt-upstream", CreatedAt: 1}
	if err := server.responsesState.saveResponse(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if code := serve(server, http.MethodDelete, "/v1/responses/resp_%2541", nil); code != http.StatusOK {
			t.Fatalf("DELETE %d answered %d", i+1, code)
		}
	}
	if code := serve(server, http.MethodGet, "/v1/conversations/conv_project/items/page%2F1", nil); code != http.StatusOK {
		t.Fatalf("GET of a provider's conversation item answered %d", code)
	}
	if got := strings.Join(calls(), ", "); got != "DELETE /v1/responses/resp_%2541, GET /v1/conversations/conv_project/items/page%2F1" {
		t.Fatalf("provider calls %s", got)
	}

	for _, tc := range []struct{ family, base, path, want string }{
		{"responses", "http://p.local/v1/responses", "/v1/responses/resp_a%3Fb%23c%20d/input_items?limit=1&after=item_%2541", "/v1/responses/resp_a%3Fb%23c%20d/input_items?limit=1&after=item_%2541"},
		{"conversations", "http://p.local/argoapi/v1/responses/", "/v1/conversations/conv_%2541/items", "/argoapi/v1/conversations/conv_%2541/items"},
		{"conversations", "http://p.local/v1/responses", "/v1/%63onversations/conv_project/items/page%2F1", "/v1/conversations/conv_project/items/page%2F1"},
		{"responses", "http://p.local/v1/responses?deployment=standard", "/v1/responses/resp_x/input_items?limit=2", "/v1/responses/resp_x/input_items?deployment=standard&limit=2"},
	} {
		target, err := rawLifecycleURL(tc.base, httptest.NewRequest(http.MethodGet, tc.path, nil), tc.family)
		if err != nil {
			t.Fatal(err)
		}
		if u, err := url.Parse(target); err != nil || u.RequestURI() != tc.want {
			t.Errorf("%s goes to %s (%v)", tc.path, target, err)
		}
	}
}

// No write replaces a record an older apiproxy wrote, whatever ID it names.
// That apiproxy named a record's file by the ID as it is: with its record of
// resp_%41 in the file this one names for resp_A, or its record of
// resp_same, both still queued, publishing or creating resp_A or resp_same
// fails, and so does saving a conversation of that name, and the file keeps
// the older record.
func TestAnOlderRecordIsNotWrittenOver(t *testing.T) {
	for _, tc := range []struct{ older, id string }{{"resp_%41", "resp_A"}, {"resp_same", "resp_same"}} {
		t.Run(tc.older, func(t *testing.T) {
			server := newServerAnsweringAs(t, tc.id)
			state := server.responsesState
			if err := state.ensure(); err != nil {
				t.Fatal(err)
			}
			for _, dir := range []string{state.responsesDir, state.conversationsDir} {
				old := fmt.Sprintf(`{"version": 1, "id": %q, "status": "queued", "session_path": "/old/0001"}`, tc.older)
				if err := os.WriteFile(filepath.Join(dir, tc.older+".json"), []byte(old), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if code := serve(server, http.MethodPost, "/v1/responses", map[string]interface{}{"model": "claude-test", "input": "q"}); code != http.StatusInternalServerError {
				t.Errorf("publishing over the older record answered %d", code)
			}
			ctx := context.Background()
			if err := state.saveResponse(ctx, &responseRecord{ID: tc.id, Object: "response", Status: "queued"}); !errors.Is(err, errResponsesStateOccupied) {
				t.Errorf("creating over the older record: %v", err)
			}
			err := state.withConversationLock(ctx, tc.id, func() error {
				return state.saveConversationLocked(tc.id, &conversationRecord{ID: tc.id, Object: "conversation"})
			})
			if !errors.Is(err, errResponsesStateOccupied) {
				t.Errorf("saving a conversation over the older record: %v", err)
			}
			for _, path := range []string{state.responsePath(tc.id), state.conversationPath(tc.id)} {
				data, err := os.ReadFile(path)
				if err != nil || !strings.Contains(string(data), `"version": 1, "id": "`+tc.older+`"`) {
					t.Errorf("%s holds %s (%v)", path, data, err)
				}
			}
		})
	}
}

// An ID whose spelled name would make its lock file's name too long for the
// file system is named by the digest of its bytes, under a prefix no spelled
// name has: responses with such IDs are stored, found and deleted, and a
// lookup of one only the provider has reaches the provider.
func TestResponsesWithLongIDsKeepTheirRecords(t *testing.T) {
	longest := "resp_" + strings.Repeat("A", 80)
	if name := stateFileName(longest); len(name) != maxStateFileName || !strings.HasPrefix(name, "resp_%41%41") {
		t.Errorf("%s is named %s", longest, name)
	}
	names := map[string]string{}
	for _, id := range []string{longest + "A", longest + "B", longest + "a", strings.Repeat("x", 4096)} {
		name := stateFileName(id)
		if !strings.HasPrefix(name, digestFileNamePrefix) || len(name) != len(digestFileNamePrefix)+64 || names[name] != "" {
			t.Errorf("%s is named %s", id, name)
		}
		names[name] = id
		if spelled, err := url.PathUnescape(name); err == nil && stateFileName(spelled) == name {
			t.Errorf("the ID %q is named %s as well", spelled, name)
		}
	}
	// An ID too long to spell is named without spelling it.
	huge, name := strings.Repeat("x", 4096), ""
	if n := testing.AllocsPerRun(10, func() { name = stateFileName(huge) }); n > 2 || names[name] != huge {
		t.Errorf("naming an ID of 4 KiB allocates %v times, and names it %s", n, name)
	}

	long, longer := longest+"A", longest+"AA"
	server := newServerAnsweringAs(t, long, longer)
	for _, want := range []string{long, longer} {
		if got := responseID(t, postResponses(t, server, map[string]interface{}{"model": "claude-test", "input": "q"})); got != want {
			t.Fatalf("the response is %s, want %s", got, want)
		}
	}
	for _, id := range []string{long, longer} {
		got := requestJSON(t, server, http.MethodGet, "/v1/responses/"+id, nil)
		if got["id"] != id || !strings.Contains(fmt.Sprint(got["output"]), "the answer of "+id) {
			t.Errorf("GET %s answered %v", id, got)
		}
		if code := serve(server, http.MethodDelete, "/v1/responses/"+id, nil); code != http.StatusOK {
			t.Errorf("DELETE %s answered %d", id, code)
		}
	}

	passthrough, calls := newPassthroughServer(t)
	if code := serve(passthrough, http.MethodGet, "/v1/responses/"+longer, nil); code != http.StatusOK {
		t.Errorf("GET of a response only the provider has answered %d", code)
	}
	if got := strings.Join(calls(), ", "); got != "GET /v1/responses/"+longer {
		t.Errorf("provider calls %s", got)
	}
}

// A writer checks a conversation's ID before it looks for the record.
func TestAnInvalidConversationIDIsRefused(t *testing.T) {
	server, _ := newBranchingServer(t)
	for _, req := range []struct{ method, path string }{
		{http.MethodDelete, "/v1/conversations/conv..obsolete/items/item_0000"},
		{http.MethodPost, "/v1/conversations/conv..obsolete"},
	} {
		if code := serve(server, req.method, req.path, map[string]interface{}{"metadata": map[string]interface{}{}}); code != http.StatusBadRequest {
			t.Errorf("%s %s answered %d", req.method, req.path, code)
		}
	}
}

// Requests for a response or conversation this proxy does not store leave
// no lock file.
func TestRequestsForUnknownRecordsLeaveNoLockFiles(t *testing.T) {
	server, _ := newBranchingServer(t)
	for _, req := range []struct {
		method, path string
		body         interface{}
	}{
		{http.MethodDelete, "/v1/responses/resp_unknown", nil},
		{http.MethodPost, "/v1/responses/resp_unknown/cancel", nil},
		{http.MethodPost, "/v1/conversations/conv_unknown", map[string]interface{}{"metadata": map[string]interface{}{"topic": "x"}}},
		{http.MethodDelete, "/v1/conversations/conv_unknown", nil},
		{http.MethodPost, "/v1/conversations/conv_unknown/items", map[string]interface{}{"items": []interface{}{map[string]interface{}{"type": "message", "role": "user", "content": "x"}}}},
		{http.MethodDelete, "/v1/conversations/conv_unknown/items/item_0000", nil},
	} {
		if code := serve(server, req.method, req.path, req.body); code != http.StatusNotFound {
			t.Errorf("%s %s answered %d", req.method, req.path, code)
		}
	}
	ctx := context.Background()
	if _, ok, err := server.responsesState.cancelResponseIfPending(ctx, "resp_unknown", nil); ok || err != nil {
		t.Errorf("cancelling an unknown response: %t %v", ok, err)
	}
	if err := server.responsesState.updateResponseStatusIfPending(ctx, "resp_unknown", "in_progress", nil); err != nil {
		t.Errorf("updating an unknown response: %v", err)
	}
	for _, dir := range []string{server.responsesState.responsesDir, server.responsesState.conversationsDir} {
		entries, err := os.ReadDir(dir)
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		for _, entry := range entries {
			t.Errorf("%s holds %s", dir, entry.Name())
		}
	}
}

// Records written by an older apiproxy read as not found.
func TestOlderResponseRecordsReadAsNotFound(t *testing.T) {
	server, _ := newBranchingServer(t)
	if err := server.responsesState.ensure(); err != nil {
		t.Fatal(err)
	}
	old, err := json.Marshal(map[string]interface{}{"version": 1, "id": "resp_old", "object": "response", "status": "completed", "session_path": "/old/0001", "message_id": "0002"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(server.responsesState.responsesDir, "resp_old.json"), old, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := server.responsesState.loadResponse("resp_old"); ok || err != nil {
		t.Fatalf("older record: ok %v: %v", ok, err)
	}
	status, body := requestJSONStatus(t, server, http.MethodPost, "/v1/responses", map[string]interface{}{"model": "claude-test", "input": "next", "previous_response_id": "resp_old"})
	if status == http.StatusOK || !strings.Contains(string(body), "not found") {
		t.Fatalf("continuing an older record: %d %s", status, body)
	}
	if code := serve(server, http.MethodDelete, "/v1/responses/resp_old", nil); code != http.StatusNotFound {
		t.Fatalf("deleting an older record answered %d", code)
	}
	if _, err := os.Stat(filepath.Join(server.responsesState.responsesDir, "resp_old.json.lock")); !os.IsNotExist(err) {
		t.Fatalf("deleting an older record left a lock file: %v", err)
	}
}
