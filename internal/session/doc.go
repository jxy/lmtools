// Package session stores conversations as trees of messages.
//
// # Storage
//
// A sessions directory holds numbered sessions. A session is a directory
// holding a tree of messages: every message names the one message it
// follows, its parent, and never changes after it is written. A message
// without a parent follows the session itself, which contributes nothing to
// a request, and a session can hold several such messages. The lineage of a
// message is the message and its ancestors, oldest first; a request that
// continues after a message carries that lineage. Messages with the same
// parent are alternatives. A branch has no name of its own: its messages
// name it.
//
//	<sessions>/
//	  sessions.json             the next session number
//	  sessions.lock             held while a session number is reserved
//	  0001/                     a session
//	    session.json            format, identity, and the next message ID
//	    reserved.json           the greatest message ID reserved
//	    0001.json               metadata of message 0001
//	    0001.content.json       content of message 0001
//	    .context/000a/          checkpoints at boundary message 000a
//	  0001.lock                 held by the writers of session 0001
//	  .journal/                 the invocation journal of tool calls
//
// Session numbers and message IDs are reserved before anything that uses
// them is written, and they are never reused, so a saved reference such as
// 0001/000c keeps its meaning. A reservation of message IDs is recorded in
// session.json and in reserved.json, written apart, so restoring either
// from an older copy gives no ID again. A session directory restored whole
// from an older copy brings both back, and the IDs reserved after that copy
// was made can be given again. A session number stays taken while a
// directory or a lock file carries it. A parent is always older than its
// child. A message exists once its metadata file exists: its content is
// written first and its metadata renamed into place last. Writers hold the
// session's lock. Readers take no lock, and a reader that finds a parent
// missing reports that the message is gone.
//
// Deletion removes a message after its children, each message's metadata
// before its content and checkpoints, so an interrupted deletion leaves every
// remaining lineage readable; the next deletion clears files left without
// metadata, and deleting the message again finishes its deletion. A session
// leaves the listing in one rename before its files go.
// Its lock file, beside the directory, stays, since its number never
// returns.
//
// A session has a number and an identity, ses_ and 128 random bits, which
// history refs and citations name, so a note citing a message keeps naming
// it. Two directories holding one identity, as a copy leaves them, are
// refused until one is named by number.
//
// # Settings
//
// A system record, a message of the system role holding Settings, records
// the conversation's settings: the custom system prompt or the built-in one,
// the notebook with its view and catalog snapshot, and the context settings.
// The settings at a message are those of the newest record in its lineage,
// so a branch has the settings in force at its parent. Records are never
// sent. Each session begins with one, and a run whose settings flags change
// the settings at its start point records the result before its first
// message. Other options, such as the model, tools and approvals, apply to
// one run; the built-in prompt follows the tools the run advertises.
//
// # Runs
//
// A run continues after one message, its start point. Prepare builds the
// request it sends, writing nothing: the lineage under the settings in force
// there, with the run's change applied, then its developer instruction and
// prompt. A run without a prompt answers the start point, which must end
// with a question or tool results. Once the provider has answered,
// Plan.Commit writes a settings record when the settings changed, the
// instruction and the prompt as one batch, dated when the plan was made, and
// returns the Session cursor at the last message it saved. Every later write
// of the run follows the cursor's head, so another run writing after the
// same message makes a separate branch. A batch that stops part way returns
// a SaveError naming the last message it saved and how to continue. The
// cursor keeps the history its plan was made from and each message it
// saves, so a run reads every message once.
//
// Tool calls pending at the start point are resolved before the request is
// prepared, by Session.ResolvePending, and their results saved, so retrying
// a failed request runs nothing again. The invocation journal records each
// call's ownership, start and outcome under the sessions directory, where
// every branch holding the call finds it.
//
// # Checkpoints
//
// The context engine reaches checkpoints through a Transcript. A checkpoint
// is saved beside the message its boundary follows and keyed by that message
// and a tag naming the settings that shaped it. Checkpoints and receipts
// count the stored messages they cover, so requests with and without a
// system message share them. The first publication for a boundary and tag
// wins, and a boundary whose message was deleted takes none.
//
// A directory with a session number and no session.json holds a session in
// the format this package replaced. Only listing and deletion accept it.
package session
