package infra

import (
	"context"
	"testing"
)

func TestNetmapChangedSubject(t *testing.T) {
	got := NetmapChangedSubject("node-a")
	want := "lattice.signals.peers.node-a.netmap"
	if got != want {
		t.Fatalf("NetmapChangedSubject(%q) = %q, want %q", "node-a", got, want)
	}
}

type fakeSignalService struct {
	published []struct {
		subject string
		data    []byte
	}
}

func (f *fakeSignalService) Send(_ context.Context, _ PeerID, _ []byte) error { return nil }
func (f *fakeSignalService) Request(_ context.Context, _, _ string, _ []byte) ([]byte, error) {
	return nil, nil
}
func (f *fakeSignalService) Service(_, _ string, _ func([]byte) ([]byte, error)) {}
func (f *fakeSignalService) Flush() error                                        { return nil }
func (f *fakeSignalService) Close() error                                        { return nil }
func (f *fakeSignalService) Publish(_ context.Context, subject string, data []byte) error {
	f.published = append(f.published, struct {
		subject string
		data    []byte
	}{subject, data})
	return nil
}

func TestPublishNetmapChanged(t *testing.T) {
	f := &fakeSignalService{}
	if err := PublishNetmapChanged(context.Background(), f, "node-a"); err != nil {
		t.Fatalf("PublishNetmapChanged: %v", err)
	}
	if len(f.published) != 1 || f.published[0].subject != "lattice.signals.peers.node-a.netmap" {
		t.Fatalf("unexpected publish calls: %+v", f.published)
	}
}

func TestPublishNetmapChangedNilSignalIsNoop(t *testing.T) {
	if err := PublishNetmapChanged(context.Background(), nil, "node-a"); err != nil {
		t.Fatalf("PublishNetmapChanged with nil signal should no-op, got err: %v", err)
	}
}
