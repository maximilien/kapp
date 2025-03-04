package app

import (
	"k8s.io/apimachinery/pkg/labels"
)

type App interface {
	Name() string
	Namespace() string
	Meta() (AppMeta, error)
	LabelSelector() (labels.Selector, error)

	CreateOrUpdate(map[string]string) error
	Exists() (bool, error)
	Delete() error
	Rename(string) error

	// Sorted as first is oldest
	Changes() ([]Change, error)
	LastChange() (Change, error)
	BeginChange(ChangeMeta) (Change, error)
	GCChanges(max int, reviewFunc func(changesToDelete []Change) error) (int, int, error)
}

type Change interface {
	Name() string
	Meta() ChangeMeta

	Fail() error
	Succeed() error

	Delete() error
}
