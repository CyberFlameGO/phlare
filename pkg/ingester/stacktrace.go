package ingester

import (
	"fmt"

	"github.com/pyroscope-io/pyroscope/pkg/structs/flamebearer"
	"github.com/xlab/treeprint"
)

type location struct {
	function string
}

type stack struct {
	locations []location
	value     int64
}

type node struct {
	parent      *node
	children    []*node
	self, total int64
	name        string
}

func (n *node) Add(name string, self, total int64) *node {
	new := &node{
		parent: n,
		name:   name,
		self:   self,
		total:  total,
	}
	n.children = append(n.children, new)
	return new
}

func (n *node) Clone() *node {
	new := *n
	return &new
}

type tree struct {
	root []*node
}

func (t *tree) Add(name string, self, total int64) *node {
	new := &node{
		name:  name,
		self:  self,
		total: total,
	}
	t.root = append(t.root, new)
	return new
}

func NewTree() *tree {
	return &tree{}
}

func (t tree) String() string {
	type branch struct {
		nodes []*node
		treeprint.Tree
	}
	tree := treeprint.New()
	for _, n := range t.root {
		b := tree.AddBranch(fmt.Sprintf("%s: self %d total %d", n.name, n.self, n.total))
		remaining := append([]*branch{}, &branch{nodes: n.children, Tree: b})
		for len(remaining) > 0 {
			current := remaining[0]
			remaining = remaining[1:]
			for _, n := range current.nodes {
				if len(n.children) > 0 {
					remaining = append(remaining, &branch{nodes: n.children, Tree: current.Tree.AddBranch(fmt.Sprintf("%s: self %d total %d", n.name, n.self, n.total))})
				} else {
					current.Tree.AddNode(fmt.Sprintf("%s: self %d total %d", n.name, n.self, n.total))
				}
			}
		}
	}
	return tree.String()
}

func mergeTree(dst, src *tree) {
	// walk src and insert src's nodes into dst
	for _, rootNode := range src.root {
		parent, found, toMerge := findNodeOrParent(dst.root, rootNode)
		if found == nil {
			if parent == nil {
				dst.root = append(dst.root, toMerge)
				continue
			}
			toMerge.parent = parent
			parent.children = append(parent.children, toMerge)
			for p := parent; p != nil; p = p.parent {
				p.total = p.total + toMerge.total
			}
			continue
		}
		found.total = found.total + toMerge.self
		found.self = found.self + toMerge.self
		for p := found.parent; p != nil; p = p.parent {
			p.total = p.total + toMerge.total
		}
	}
}

// Walks into root nodes to find a node, return the latest common parent visited.
func findNodeOrParent(root []*node, new *node) (parent, found, toMerge *node) {
	current := new
	var lastParent *node
	remaining := append([]*node{}, root...)
	for len(remaining) > 0 {
		n := remaining[0]
		remaining = remaining[1:]
		// we found the common parent so we go down
		if n.name == current.name {
			// we reach the end of the new path to find.
			if len(current.children) == 0 {
				return lastParent, n, current
			}
			lastParent = n
			remaining = n.children
			current = current.children[0]
			continue
		}
	}

	return lastParent, nil, current
}

func strackToTree(stack stack) *tree {
	t := &tree{}
	if len(stack.locations) == 0 {
		return t
	}
	current := &node{
		self:  stack.value,
		total: stack.value,
		name:  stack.locations[0].function,
	}
	if len(stack.locations) == 1 {
		t.root = append(t.root, current)
		return t
	}
	remaining := stack.locations[1:]
	for len(remaining) > 0 {

		location := remaining[0]
		name := location.function
		remaining = remaining[1:]

		// This pack node with the same name as the next location
		// Disable for now but we might want to introduce it if we find it useful.
		// for len(remaining) != 0 {
		// 	if remaining[0].function == name {
		// 		remaining = remaining[1:]
		// 		continue
		// 	}
		// 	break
		// }

		parent := &node{
			children: []*node{current},
			total:    current.total,
			name:     name,
		}
		current.parent = parent
		current = parent
	}
	t.root = []*node{current}
	return t
}

func stacksToTree(stacks []stack) *tree {
	t := &tree{}
	for _, stack := range stacks {
		if stack.value == 0 {
			continue
		}
		if t == nil {
			t = strackToTree(stack)
			continue
		}
		mergeTree(t, strackToTree(stack))
	}
	return t
}

func (t *tree) toFlamebearer() *flamebearer.FlamebearerV1 {
	var total, max int64
	for _, node := range t.root {
		total += node.total
	}
	names := []string{}
	nameLocationCache := map[string]int{}
	res := [][]int{}

	xOffsets := []int{0}

	levels := []int{0}

	nodes := []*node{{children: t.root, total: total}}

	for len(nodes) > 0 {
		current := nodes[0]
		nodes = nodes[1:]

		xOffset := xOffsets[0]
		xOffsets = xOffsets[1:]

		level := levels[0]
		levels = levels[1:]
		if current.self > max {
			max = current.self
		}
		var i int
		var ok bool
		name := current.name
		if i, ok = nameLocationCache[name]; !ok {
			i = len(names)
			if i == 0 {
				name = "total"
			}
			nameLocationCache[name] = i
			names = append(names, name)
		}

		if level == len(res) {
			res = append(res, []int{})
		}

		// i+0 = x offset
		// i+1 = total
		// i+2 = self
		// i+3 = index in names array
		res[level] = append([]int{xOffset, int(current.total), int(current.self), i}, res[level]...)
		xOffset += int(current.self)

		for _, child := range current.children {
			xOffsets = append([]int{xOffset}, xOffsets...)
			levels = append([]int{level + 1}, levels...)
			nodes = append([]*node{child}, nodes...)
			xOffset += int(child.total)
		}
	}
	// delta encode xoffsets
	for _, l := range res {
		prev := 0
		for i := 0; i < len(l); i += 4 {
			l[i] -= prev
			prev += l[i] + l[i+1]
		}
	}
	return &flamebearer.FlamebearerV1{
		Names:    names,
		Levels:   res,
		NumTicks: int(total),
		MaxSelf:  int(max),
	}
}