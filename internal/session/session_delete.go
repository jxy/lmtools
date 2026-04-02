package session

import (
	stdErrors "errors"
	"fmt"
	"lmtools/internal/errors"
	"os"
	"path/filepath"
	"strconv"
)

// DeleteNode deletes a node (session, branch, or message) and all its descendants.
func DeleteNode(nodePath string) error {
	nodePath = DefaultManager().ResolveSessionPath(nodePath)

	if !DefaultManager().IsWithinSessionsDir(nodePath) {
		return errors.WrapError("validate path", stdErrors.New("invalid path: must be within sessions directory"))
	}

	info, err := os.Stat(nodePath)
	if err == nil && info.IsDir() {
		// Directory node exists, continue.
	} else {
		dir := filepath.Dir(nodePath)
		msgID := filepath.Base(nodePath)

		if !IsValidMessageID(msgID) {
			return errors.WrapError("delete node", fmt.Errorf("node not found: %s", nodePath))
		}

		if _, err := os.Stat(buildMessageFilePaths(dir, msgID).JSONPath); err != nil {
			return errors.WrapError("delete node", fmt.Errorf("node not found: %s", nodePath))
		}
	}

	rootSession := GetRootSession(nodePath)

	return WithSessionLock(rootSession, 0, func() error {
		info, err := os.Stat(nodePath)
		if err == nil && info.IsDir() {
			return os.RemoveAll(nodePath)
		}

		dir := filepath.Dir(nodePath)
		msgID := filepath.Base(nodePath)

		var msgNum int
		if _, err := fmt.Sscanf(msgID, "%x", &msgNum); err != nil {
			return errors.WrapError("validate message ID", fmt.Errorf("invalid message ID: %s", msgID))
		}

		return deleteMessageAndDescendants(dir, msgNum)
	})
}

// deleteMessageAndDescendants deletes a message and all subsequent messages/branches.
func deleteMessageAndDescendants(dirPath string, msgNum int) error {
	msgIDs, err := listMessages(dirPath)
	if err != nil {
		return errors.WrapError("list messages", err)
	}

	for _, msgID := range msgIDs {
		num, err := strconv.ParseUint(msgID, 16, 64)
		if err != nil {
			continue
		}

		if int(num) >= msgNum {
			paths := buildMessageFilePaths(dirPath, msgID)
			_ = os.Remove(paths.TxtPath)
			_ = os.Remove(paths.ToolsPath)
			if err := os.Remove(paths.JSONPath); err != nil && !os.IsNotExist(err) {
				return errors.WrapError("delete metadata file", err)
			}
		}
	}

	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return errors.WrapError("read directory", err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		name := entry.Name()
		if ok, branchMsgID, _ := IsSiblingDir(name); ok {
			branchNum, err := strconv.ParseUint(branchMsgID, 16, 64)
			if err != nil {
				continue
			}

			if int(branchNum) > msgNum {
				branchPath := filepath.Join(dirPath, name)
				if err := os.RemoveAll(branchPath); err != nil {
					return errors.WrapError("delete sibling branch "+name, err)
				}
			}
		}
	}

	return nil
}
