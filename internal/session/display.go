package session

import (
	"fmt"
	"sort"
)

// ShowSessionTree displays the conversation tree for a session
func ShowSessionTree(sessionPath string) error {
	// Get session info
	sessionID := GetSessionID(sessionPath)

	// Build the conversation tree
	nodes, err := buildTree(sessionPath)
	if err != nil {
		return fmt.Errorf("failed to build conversation tree: %w", err)
	}

	if len(nodes) == 0 {
		fmt.Println("No messages in session.")
		return nil
	}

	// Print session header
	fmt.Printf("Session %s:\n", sessionID)

	// Print the tree
	for i, node := range nodes {
		isLast := i == len(nodes)-1
		printTreeNode(node, "", isLast)
	}

	return nil
}

// printTreeNode recursively prints the message tree
func printTreeNode(node *TreeNode, prefix string, isLast bool) {
	if node == nil {
		return
	}

	// Determine the connector
	connector := "├── "
	if isLast {
		connector = "└── "
	}

	// Format the node display
	roleDisplay := fmt.Sprintf("[%s]", node.Message.Role)
	if node.Message.Role == "assistant" && node.Message.Model != "" {
		roleDisplay = fmt.Sprintf("[%s(%s)]", node.Message.Role, node.Message.Model)
	}

	// Print the node
	fmt.Printf("%s%s%s %s %s\n", prefix, connector, node.Message.ID, roleDisplay,
		node.Message.Timestamp.Format("2006-01-02 15:04:05"))

	// Prepare prefix for children
	childPrefix := prefix
	if isLast {
		childPrefix += "    "
	} else {
		childPrefix += "│   "
	}

	// Sort children by ID for consistent display
	children := make([]*TreeNode, 0, len(node.Children))
	children = append(children, node.Children...)
	sort.Slice(children, func(i, j int) bool {
		return children[i].Message.ID < children[j].Message.ID
	})

	// Print children
	for i, child := range children {
		isLastChild := i == len(children)-1
		printTreeNode(child, childPrefix, isLastChild)
	}
}
