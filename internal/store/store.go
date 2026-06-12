// Package store persists per-user subreddit subscriptions in a bbolt
// database. Layout: bucket "subscriptions" → nested bucket per user ID
// (the OIDC subject) → one key per subscribed subreddit.
package store

import (
	"fmt"
	"sort"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"
)

var bucketSubscriptions = []byte("subscriptions")

// Store wraps the bbolt database.
type Store struct {
	db *bolt.DB
}

// Open opens (creating if needed) the database at path.
func Open(path string) (*Store, error) {
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: 2 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("opening database %s: %w", path, err)
	}
	err = db.Update(func(tx *bolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists(bucketSubscriptions)
		return err
	})
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("initializing database: %w", err)
	}
	return &Store{db: db}, nil
}

// Close releases the database file.
func (s *Store) Close() error { return s.db.Close() }

// NormalizeSubreddit canonicalizes user input such as "/r/Golang/" to
// "golang". It returns "" for input that cannot be a subreddit name.
func NormalizeSubreddit(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	name = strings.Trim(name, "/")
	name = strings.TrimPrefix(name, "r/")
	if name == "" || strings.ContainsAny(name, "/+?&# ") {
		return ""
	}
	return name
}

// Subscriptions returns the sorted list of subreddits userID follows.
func (s *Store) Subscriptions(userID string) ([]string, error) {
	var subs []string
	err := s.db.View(func(tx *bolt.Tx) error {
		user := tx.Bucket(bucketSubscriptions).Bucket([]byte(userID))
		if user == nil {
			return nil
		}
		return user.ForEach(func(k, _ []byte) error {
			subs = append(subs, string(k))
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(subs)
	return subs, nil
}

// IsSubscribed reports whether userID follows the given subreddit.
func (s *Store) IsSubscribed(userID, subreddit string) (bool, error) {
	subreddit = NormalizeSubreddit(subreddit)
	var found bool
	err := s.db.View(func(tx *bolt.Tx) error {
		user := tx.Bucket(bucketSubscriptions).Bucket([]byte(userID))
		found = user != nil && user.Get([]byte(subreddit)) != nil
		return nil
	})
	return found, err
}

// Subscribe adds a subreddit to the user's subscriptions.
func (s *Store) Subscribe(userID, subreddit string) error {
	subreddit = NormalizeSubreddit(subreddit)
	if subreddit == "" {
		return fmt.Errorf("invalid subreddit name")
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		user, err := tx.Bucket(bucketSubscriptions).CreateBucketIfNotExists([]byte(userID))
		if err != nil {
			return err
		}
		return user.Put([]byte(subreddit), []byte{1})
	})
}

// Unsubscribe removes a subreddit from the user's subscriptions.
func (s *Store) Unsubscribe(userID, subreddit string) error {
	subreddit = NormalizeSubreddit(subreddit)
	if subreddit == "" {
		return fmt.Errorf("invalid subreddit name")
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		user := tx.Bucket(bucketSubscriptions).Bucket([]byte(userID))
		if user == nil {
			return nil
		}
		return user.Delete([]byte(subreddit))
	})
}
