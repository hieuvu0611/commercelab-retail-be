package domain

import (
	"sort"
	"strings"
)

// CategoryNode is a category in the public tree.
type CategoryNode struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Slug      string         `json:"slug"`
	Path      string         `json:"path"`
	Depth     int            `json:"depth"`
	SortOrder int            `json:"sortOrder"`
	Children  []CategoryNode `json:"children"`
}

// BuildCategoryTree returns the storefront tree: inactive categories and everything below them are left out,
// siblings are ordered by sortOrder, then name.
func BuildCategoryTree(cats []Category) []CategoryNode {
	active := make(map[string]Category, len(cats))
	for _, c := range cats {
		active[c.ID] = c
	}
	byParent := map[string][]Category{}
	for _, c := range cats {
		if c.IsActive {
			byParent[c.ParentID] = append(byParent[c.ParentID], c)
		}
	}
	var build func(parentID string) []CategoryNode
	build = func(parentID string) []CategoryNode {
		siblings := byParent[parentID]
		sort.SliceStable(siblings, func(i, j int) bool {
			if siblings[i].SortOrder != siblings[j].SortOrder {
				return siblings[i].SortOrder < siblings[j].SortOrder
			}
			return siblings[i].Name < siblings[j].Name
		})
		nodes := make([]CategoryNode, 0, len(siblings))
		for _, c := range siblings {
			nodes = append(nodes, CategoryNode{
				ID: c.ID, Name: c.Name, Slug: c.Slug, Path: c.Path, Depth: c.Depth, SortOrder: c.SortOrder,
				Children: build(c.ID),
			})
		}
		return nodes
	}
	return build("")
}

// FindCategoryNode returns the node at path ("/backpacks/city") from a tree built by BuildCategoryTree.
func FindCategoryNode(tree []CategoryNode, path string) (CategoryNode, bool) {
	for _, n := range tree {
		if n.Path == path {
			return n, true
		}
		if strings.HasPrefix(path, n.Path+"/") {
			return FindCategoryNode(n.Children, path)
		}
	}
	return CategoryNode{}, false
}
