package infoitem

import (
	"context"
	"testing"
)

type stubReader struct{}

func (stubReader) Region(context.Context, RegionQuery) (RegionResult, error) {
	return RegionResult{}, nil
}
func (stubReader) Associate(context.Context, AssociationQuery) (AssociationResult, error) {
	return AssociationResult{}, nil
}
func (stubReader) ContextSlice(context.Context, ContextQuery) (ContextResult, error) {
	return ContextResult{}, nil
}
func (stubReader) GetByID(context.Context, string, string) (Item, error) {
	return Item{}, ErrNotFound
}

func TestReaderRegistryLifecycle(t *testing.T) {
	cancel := RegisterReader(stubReader{})
	if DefaultReader() == nil {
		t.Fatal("expected registered reader")
	}
	cancel()
	if DefaultReader() != nil {
		t.Fatal("expected reader to be unregistered")
	}
	cancel()
}
