package store

import (
	"path/filepath"
	"reflect"
	"testing"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestSubscriptions(t *testing.T) {
	s := openTestStore(t)

	subs, err := s.Subscriptions("alice")
	if err != nil || len(subs) != 0 {
		t.Fatalf("expected empty subscriptions, got %v, %v", subs, err)
	}

	for _, sub := range []string{"golang", "/r/Selfhosted/", "NixOS"} {
		if err := s.Subscribe("alice", sub); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Subscribe("bob", "rust"); err != nil {
		t.Fatal(err)
	}

	subs, err = s.Subscriptions("alice")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"golang", "nixos", "selfhosted"}
	if !reflect.DeepEqual(subs, want) {
		t.Fatalf("got %v, want %v", subs, want)
	}

	if ok, _ := s.IsSubscribed("alice", "Selfhosted"); !ok {
		t.Fatal("expected alice subscribed to selfhosted")
	}
	if ok, _ := s.IsSubscribed("alice", "rust"); ok {
		t.Fatal("alice should not be subscribed to rust")
	}

	if err := s.Unsubscribe("alice", "golang"); err != nil {
		t.Fatal(err)
	}
	subs, _ = s.Subscriptions("alice")
	if !reflect.DeepEqual(subs, []string{"nixos", "selfhosted"}) {
		t.Fatalf("after unsubscribe got %v", subs)
	}
}

func TestNormalizeSubreddit(t *testing.T) {
	cases := map[string]string{
		"golang":      "golang",
		" /r/GoLang/": "golang",
		"r/nixos":     "nixos",
		"":            "",
		"a+b":         "",
		"a/b":         "",
	}
	for in, want := range cases {
		if got := NormalizeSubreddit(in); got != want {
			t.Errorf("NormalizeSubreddit(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSubscribeInvalid(t *testing.T) {
	s := openTestStore(t)
	if err := s.Subscribe("alice", "a+b"); err == nil {
		t.Fatal("expected error for invalid subreddit name")
	}
}
