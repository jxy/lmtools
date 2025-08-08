package session

import (
	"path/filepath"
	"testing"
	"time"
)

func TestBasicConversationFlow(t *testing.T) {
	WithTestSessionDir(t, func(sessionsDir string) {
		// 1. Create new session
		session, err := CreateSession()
		if err != nil {
			t.Fatalf("Failed to create session: %v", err)
		}

		// 2. Add user message
		userMsg := Message{
			Role:      "user",
			Content:   "Hello, how are you?",
			Timestamp: time.Now(),
		}
		_, userMsgID, err := AppendMessage(session, userMsg)
		if err != nil {
			t.Fatalf("Failed to append user message: %v", err)
		}

		if userMsgID != "0000" {
			t.Errorf("Expected first message ID to be 0000, got %s", userMsgID)
		}

		// 3. Add assistant response
		assistantMsg := Message{
			Role:      "assistant",
			Content:   "I'm doing well, thank you! How can I help you today?",
			Timestamp: time.Now(),
			Model:     "test-model",
		}
		_, assistantMsgID, err := AppendMessage(session, assistantMsg)
		if err != nil {
			t.Fatalf("Failed to append assistant message: %v", err)
		}

		if assistantMsgID != "0001" {
			t.Errorf("Expected second message ID to be 0001, got %s", assistantMsgID)
		}

		// 4. Verify lineage
		messages, err := GetLineage(session.Path)
		if err != nil {
			t.Fatalf("Failed to get lineage: %v", err)
		}

		if len(messages) != 2 {
			t.Errorf("Expected 2 messages in lineage, got %d", len(messages))
		}

		if messages[0].Content != "Hello, how are you?" {
			t.Errorf("First message content mismatch: %s", messages[0].Content)
		}

		if messages[1].Content != "I'm doing well, thank you! How can I help you today?" {
			t.Errorf("Second message content mismatch: %s", messages[1].Content)
		}

		// 5. Reload session and verify persistence
		reloaded, err := LoadSession(session.Path)
		if err != nil {
			t.Fatalf("Failed to reload session: %v", err)
		}

		reloadedMessages, err := GetLineage(reloaded.Path)
		if err != nil {
			t.Fatalf("Failed to get reloaded lineage: %v", err)
		}

		assertLineageEqual(t, messages, reloadedMessages)
	})
}

func TestBranchingWorkflow(t *testing.T) {
	WithTestSessionDir(t, func(sessionsDir string) {
		// 1. Create session with 3 messages
		session, err := CreateSession()
		if err != nil {
			t.Fatalf("Failed to create session: %v", err)
		}

		// Add messages
		messages := []Message{
			{Role: "user", Content: "1: Initial question", Timestamp: time.Now()},
			{Role: "assistant", Content: "2: Initial response", Timestamp: time.Now(), Model: "test-model"},
			{Role: "user", Content: "3: Follow-up question", Timestamp: time.Now()},
		}

		for _, msg := range messages {
			if _, _, err := AppendMessage(session, msg); err != nil {
				t.Fatalf("Failed to append message: %v", err)
			}
		}

		// 2. Branch from message 0001 (assistant response)
		branchPath, err := CreateSibling(session.Path, "0001")
		if err != nil {
			t.Fatalf("Failed to create branch: %v", err)
		}

		expectedBranchPath := filepath.Join(session.Path, "0001.s.0000")
		if branchPath != expectedBranchPath {
			t.Errorf("Expected branch path %s, got %s", expectedBranchPath, branchPath)
		}

		branch, err := LoadSession(branchPath)
		if err != nil {
			t.Fatalf("Failed to load branch session: %v", err)
		}

		// 3. Add messages to branch
		branchMessages := []Message{
			{Role: "user", Content: "alt-3: Alternative follow-up", Timestamp: time.Now()},
			{Role: "assistant", Content: "alt-4: Alternative response", Timestamp: time.Now(), Model: "test-model"},
		}

		for _, msg := range branchMessages {
			if _, _, err := AppendMessage(branch, msg); err != nil {
				t.Fatalf("Failed to append branch message: %v", err)
			}
		}

		// 4. Verify main lineage (should have all 3 original messages)
		mainMessages, err := GetLineage(session.Path)
		if err != nil {
			t.Fatalf("Failed to get main lineage: %v", err)
		}

		if len(mainMessages) != 3 {
			t.Errorf("Expected 3 messages in main lineage, got %d", len(mainMessages))
		}

		// Verify main lineage content
		for i, msg := range mainMessages {
			if msg.Content != messages[i].Content {
				t.Errorf("Main message %d content mismatch: expected %s, got %s", i, messages[i].Content, msg.Content)
			}
		}

		// 5. Verify branch lineage (should have messages 0, 1, then branch messages)
		branchLineage, err := GetLineage(branch.Path)
		if err != nil {
			t.Fatalf("Failed to get branch lineage: %v", err)
		}

		if len(branchLineage) != 3 {
			t.Errorf("Expected 3 messages in branch lineage, got %d", len(branchLineage))
		}

		// Check branch lineage content
		expectedBranchContent := []string{
			"1: Initial question",          // 0000 from root (only message before branch point)
			"alt-3: Alternative follow-up", // branch 0000
			"alt-4: Alternative response",  // branch 0001
		}

		for i, expected := range expectedBranchContent {
			if branchLineage[i].Content != expected {
				t.Errorf("Branch message %d content mismatch: expected %s, got %s", i, expected, branchLineage[i].Content)
			}
		}
	})
}

func TestMultipleBranches(t *testing.T) {
	WithTestSessionDir(t, func(sessionsDir string) {
		// Create session with initial messages
		session, err := CreateSession()
		if err != nil {
			t.Fatalf("Failed to create session: %v", err)
		}

		// Add messages
		messages := []Message{
			{Role: "user", Content: "Question", Timestamp: time.Now()},
			{Role: "assistant", Content: "Response", Timestamp: time.Now(), Model: "test-model"},
			{Role: "user", Content: "Follow-up", Timestamp: time.Now()},
		}

		for _, msg := range messages {
			if _, _, err := AppendMessage(session, msg); err != nil {
				t.Fatalf("Failed to append message: %v", err)
			}
		}

		// Create multiple branches from the same point (message 0001)
		branch1Path, err := CreateSibling(session.Path, "0001")
		if err != nil {
			t.Fatalf("Failed to create first branch: %v", err)
		}

		branch2Path, err := CreateSibling(session.Path, "0001")
		if err != nil {
			t.Fatalf("Failed to create second branch: %v", err)
		}

		branch3Path, err := CreateSibling(session.Path, "0001")
		if err != nil {
			t.Fatalf("Failed to create third branch: %v", err)
		}

		// Verify branch paths
		expectedPaths := []string{
			filepath.Join(session.Path, "0001.s.0000"),
			filepath.Join(session.Path, "0001.s.0001"),
			filepath.Join(session.Path, "0001.s.0002"),
		}

		actualPaths := []string{branch1Path, branch2Path, branch3Path}

		for i, expected := range expectedPaths {
			if actualPaths[i] != expected {
				t.Errorf("Branch %d path mismatch: expected %s, got %s", i, expected, actualPaths[i])
			}
		}

		// Add different content to each branch
		branches := []struct {
			path    string
			content string
		}{
			{branch1Path, "Branch 1 content"},
			{branch2Path, "Branch 2 content"},
			{branch3Path, "Branch 3 content"},
		}

		for _, b := range branches {
			branchSession, err := LoadSession(b.path)
			if err != nil {
				t.Fatalf("Failed to load branch %s: %v", b.path, err)
			}

			msg := Message{
				Role:      "user",
				Content:   b.content,
				Timestamp: time.Now(),
			}

			if _, _, err := AppendMessage(branchSession, msg); err != nil {
				t.Fatalf("Failed to append to branch %s: %v", b.path, err)
			}
		}

		// Verify each branch has independent history
		for _, b := range branches {
			lineage, err := GetLineage(b.path)
			if err != nil {
				t.Fatalf("Failed to get lineage for %s: %v", b.path, err)
			}

			// Should have message 0 from root and the branch-specific message
			if len(lineage) != 2 {
				t.Errorf("Expected 2 messages in branch %s, got %d", b.path, len(lineage))
			}

			// Verify branch-specific content
			if len(lineage) >= 2 && lineage[1].Content != b.content {
				t.Errorf("Branch %s content mismatch: expected %s, got %s", b.path, b.content, lineage[1].Content)
			}
		}
	})
}

func TestNestedBranches(t *testing.T) {
	WithTestSessionDir(t, func(sessionsDir string) {
		// Create initial session
		session, err := CreateSession()
		if err != nil {
			t.Fatalf("Failed to create session: %v", err)
		}

		// Add initial messages
		messages := []Message{
			{Role: "user", Content: "Level 0 - Message 0", Timestamp: time.Now()},
			{Role: "assistant", Content: "Level 0 - Message 1", Timestamp: time.Now(), Model: "test-model"},
			{Role: "user", Content: "Level 0 - Message 2", Timestamp: time.Now()},
		}

		for _, msg := range messages {
			if _, _, err := AppendMessage(session, msg); err != nil {
				t.Fatalf("Failed to append message: %v", err)
			}
		}

		// Create first level branch from message 0001
		level1Path, err := CreateSibling(session.Path, "0001")
		if err != nil {
			t.Fatalf("Failed to create level 1 branch: %v", err)
		}

		level1Session, err := LoadSession(level1Path)
		if err != nil {
			t.Fatalf("Failed to load level 1 session: %v", err)
		}

		// Add messages to level 1 branch
		level1Messages := []Message{
			{Role: "user", Content: "Level 1 - Message 0", Timestamp: time.Now()},
			{Role: "assistant", Content: "Level 1 - Message 1", Timestamp: time.Now(), Model: "test-model"},
		}

		for _, msg := range level1Messages {
			if _, _, err := AppendMessage(level1Session, msg); err != nil {
				t.Fatalf("Failed to append level 1 message: %v", err)
			}
		}

		// Create second level branch from level 1's message 0000
		level2Path, err := CreateSibling(level1Session.Path, "0000")
		if err != nil {
			t.Fatalf("Failed to create level 2 branch: %v", err)
		}

		level2Session, err := LoadSession(level2Path)
		if err != nil {
			t.Fatalf("Failed to load level 2 session: %v", err)
		}

		// Add message to level 2 branch
		level2Message := Message{
			Role:      "user",
			Content:   "Level 2 - Message 0",
			Timestamp: time.Now(),
		}

		if _, _, err := AppendMessage(level2Session, level2Message); err != nil {
			t.Fatalf("Failed to append level 2 message: %v", err)
		}

		// Verify level 2 lineage
		level2Lineage, err := GetLineage(level2Session.Path)
		if err != nil {
			t.Fatalf("Failed to get level 2 lineage: %v", err)
		}

		// Expected lineage for level 2:
		// With bubble-up, this creates a sibling at the parent level
		// So it's a sibling of 0001, containing only messages before 0001
		expectedContent := []string{
			"Level 0 - Message 0",
			"Level 2 - Message 0",
		}

		if len(level2Lineage) != len(expectedContent) {
			t.Errorf("Expected %d messages in level 2 lineage, got %d", len(expectedContent), len(level2Lineage))
		}

		for i, expected := range expectedContent {
			if i < len(level2Lineage) && level2Lineage[i].Content != expected {
				t.Errorf("Level 2 lineage message %d mismatch: expected %s, got %s", i, expected, level2Lineage[i].Content)
			}
		}

		// Create another branch at level 2 to test deep nesting
		level3Path, err := CreateSibling(level2Session.Path, "0000")
		if err != nil {
			t.Fatalf("Failed to create level 3 branch: %v", err)
		}

		level3Session, err := LoadSession(level3Path)
		if err != nil {
			t.Fatalf("Failed to load level 3 session: %v", err)
		}

		level3Message := Message{
			Role:      "assistant",
			Content:   "Level 3 - Message 0",
			Timestamp: time.Now(),
			Model:     "test-model",
		}

		if _, _, err := AppendMessage(level3Session, level3Message); err != nil {
			t.Fatalf("Failed to append level 3 message: %v", err)
		}

		// Verify level 3 lineage
		level3Lineage, err := GetLineage(level3Session.Path)
		if err != nil {
			t.Fatalf("Failed to get level 3 lineage: %v", err)
		}

		// Expected: With bubble-up, level 3 is also a sibling of 0001
		expectedLevel3 := []string{
			"Level 0 - Message 0",
			"Level 3 - Message 0",
		}

		if len(level3Lineage) != len(expectedLevel3) {
			t.Errorf("Expected %d messages in level 3 lineage, got %d", len(expectedLevel3), len(level3Lineage))
		}

		for i, expected := range expectedLevel3 {
			if i < len(level3Lineage) && level3Lineage[i].Content != expected {
				t.Errorf("Level 3 lineage message %d mismatch: expected %s, got %s", i, expected, level3Lineage[i].Content)
			}
		}
	})
}

func TestSessionTreeBuilding(t *testing.T) {
	WithTestSessionDir(t, func(sessionsDir string) {
		// Create a complex tree structure
		session, err := CreateSession()
		if err != nil {
			t.Fatalf("Failed to create session: %v", err)
		}

		// Add root messages
		rootMessages := []Message{
			{Role: "user", Content: "Root 0", Timestamp: time.Now()},
			{Role: "assistant", Content: "Root 1", Timestamp: time.Now(), Model: "model1"},
			{Role: "user", Content: "Root 2", Timestamp: time.Now()},
			{Role: "assistant", Content: "Root 3", Timestamp: time.Now(), Model: "model2"},
		}

		for _, msg := range rootMessages {
			if _, _, err := AppendMessage(session, msg); err != nil {
				t.Fatalf("Failed to append root message: %v", err)
			}
		}

		// Create branches at different points
		// Branch 1: from message 0001
		branch1Path, err := CreateSibling(session.Path, "0001")
		if err != nil {
			t.Fatalf("Failed to create branch 1: %v", err)
		}

		branch1, _ := LoadSession(branch1Path)
		branch1Msgs := []Message{
			{Role: "user", Content: "Branch 1 - Msg 0", Timestamp: time.Now()},
			{Role: "assistant", Content: "Branch 1 - Msg 1", Timestamp: time.Now(), Model: "model3"},
		}

		for _, msg := range branch1Msgs {
			if _, _, err := AppendMessage(branch1, msg); err != nil {
				t.Fatalf("Failed to append to branch 1: %v", err)
			}
		}

		// Branch 2: also from message 0001 (sibling of branch 1)
		branch2Path, err := CreateSibling(session.Path, "0001")
		if err != nil {
			t.Fatalf("Failed to create branch 2: %v", err)
		}

		branch2, _ := LoadSession(branch2Path)
		if _, _, err := AppendMessage(branch2, Message{Role: "user", Content: "Branch 2 - Msg 0", Timestamp: time.Now()}); err != nil {
			t.Fatalf("Failed to append to branch 2: %v", err)
		}

		// Branch 3: from message 0003 (regeneration)
		branch3Path, err := CreateSibling(session.Path, "0003")
		if err != nil {
			t.Fatalf("Failed to create branch 3: %v", err)
		}

		branch3, _ := LoadSession(branch3Path)
		if _, _, err := AppendMessage(branch3, Message{Role: "assistant", Content: "Root 3 - Regenerated", Timestamp: time.Now(), Model: "model2-regen"}); err != nil {
			t.Fatalf("Failed to append to branch 3: %v", err)
		}

		// Nested branch: from branch1's message 0000
		nestedPath, err := CreateSibling(branch1.Path, "0000")
		if err != nil {
			t.Fatalf("Failed to create nested branch: %v", err)
		}

		nested, _ := LoadSession(nestedPath)
		if _, _, err := AppendMessage(nested, Message{Role: "user", Content: "Nested - Msg 0", Timestamp: time.Now()}); err != nil {
			t.Fatalf("Failed to append to nested branch: %v", err)
		}

		// Verify the tree structure exists
		// Check sibling directories
		siblings, err := findSiblings(session.Path, "0001")
		if err != nil {
			t.Fatalf("Failed to find siblings for 0001: %v", err)
		}

		// With bubble-up, the nested branch creates another sibling of 0001
		if len(siblings) != 3 {
			t.Errorf("Expected 3 siblings for message 0001 (including bubbled-up), got %d", len(siblings))
		}

		siblings, err = findSiblings(session.Path, "0003")
		if err != nil {
			t.Fatalf("Failed to find siblings for 0003: %v", err)
		}

		if len(siblings) != 1 {
			t.Errorf("Expected 1 sibling for message 0003, got %d", len(siblings))
		}

		// With bubble-up, no nested siblings are created
		nestedSiblings, err := findSiblings(branch1.Path, "0000")
		if err != nil {
			t.Fatalf("Failed to find nested siblings: %v", err)
		}

		if len(nestedSiblings) != 0 {
			t.Errorf("Expected 0 nested siblings (bubbled up), got %d", len(nestedSiblings))
		}
	})
}
