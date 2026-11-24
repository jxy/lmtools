// Package session stores conversations as trees of messages.
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
//	    0001.json               metadata of message 0001
//	    0001.content.json       content of message 0001
//	    .context/000a/          checkpoints at boundary message 000a
//	  0001.lock                 held by the writers of session 0001
//
// Session numbers and message IDs are reserved before anything that uses
// them is written, and they are never reused, so a saved reference such as
// 0001/000c keeps its meaning. A message exists once its metadata file
// exists: its content is written first and its metadata renamed into place
// last. Writers hold the session's lock. Readers take no lock, and a reader
// that finds a parent missing reports that the message is gone.
//
// A directory with a session number and no session.json holds a session in
// the format this package replaced. Only listing and deletion accept it.
package session
