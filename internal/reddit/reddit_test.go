package reddit

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

const listingFixture = `{
  "kind": "Listing",
  "data": {
    "after": "t3_next",
    "children": [
      {"kind": "t3", "data": {
        "id": "abc", "title": "Go 1.24 released", "author": "gopher",
        "subreddit": "golang", "permalink": "/r/golang/comments/abc/",
        "url": "https://go.dev/blog", "domain": "go.dev",
        "thumbnail": "https://b.thumbs.redditmedia.com/x.jpg",
        "score": 1234, "num_comments": 56, "created_utc": 1700000000,
        "is_self": false
      }},
      {"kind": "t3", "data": {
        "id": "def", "title": "A question", "author": "newbie",
        "subreddit": "golang", "thumbnail": "self",
        "score": 5, "num_comments": 2, "created_utc": 1700000100,
        "is_self": true, "selftext_html": "<p>hello</p>"
      }}
    ]
  }
}`

// commentsFixture exercises the two tricky shapes: replies as the empty
// string "" and a trailing "more" stub.
const commentsFixture = `[
  {"kind": "Listing", "data": {"children": [
    {"kind": "t3", "data": {"id": "abc", "title": "Go 1.24 released", "author": "gopher", "subreddit": "golang", "score": 1234, "num_comments": 56, "created_utc": 1700000000, "is_self": true}}
  ]}},
  {"kind": "Listing", "data": {"children": [
    {"kind": "t1", "data": {
      "id": "c1", "author": "alice", "body_html": "<p>nice</p>", "score": 10, "created_utc": 1700000200,
      "replies": {"kind": "Listing", "data": {"children": [
        {"kind": "t1", "data": {"id": "c2", "author": "bob", "body_html": "<p>agreed</p>", "score": 3, "created_utc": 1700000300, "replies": ""}}
      ]}}
    }},
    {"kind": "more", "data": {"count": 42, "parent_id": "t1_c9"}}
  ]}}
]`

func testClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c := NewClient("test-agent")
	c.BaseURL = srv.URL
	return c
}

func TestPosts(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/r/golang/hot.json" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if r.URL.Query().Get("raw_json") != "1" {
			t.Error("expected raw_json=1")
		}
		if r.Header.Get("User-Agent") != "test-agent" {
			t.Errorf("unexpected user agent %q", r.Header.Get("User-Agent"))
		}
		w.Write([]byte(listingFixture))
	})

	listing, err := c.Posts(context.Background(), "golang", "hot", ListingOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if listing.After != "t3_next" {
		t.Errorf("after = %q", listing.After)
	}
	if len(listing.Posts) != 2 {
		t.Fatalf("got %d posts", len(listing.Posts))
	}
	first := listing.Posts[0]
	if first.Title != "Go 1.24 released" || first.Score != 1234 || !first.HasThumbnail() {
		t.Errorf("unexpected post: %+v", first)
	}
	if listing.Posts[1].HasThumbnail() {
		t.Error("self thumbnail must not count as an image")
	}
}

func TestComments(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/comments/abc.json" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		w.Write([]byte(commentsFixture))
	})

	post, comments, err := c.Comments(context.Background(), "abc")
	if err != nil {
		t.Fatal(err)
	}
	if post.ID != "abc" {
		t.Errorf("post = %+v", post)
	}
	if len(comments) != 2 {
		t.Fatalf("got %d top-level comments", len(comments))
	}
	top := comments[0]
	if top.Author != "alice" || len(top.Replies) != 1 || top.Replies[0].Author != "bob" {
		t.Errorf("unexpected tree: %+v", top)
	}
	if top.PostID != "abc" || top.Replies[0].PostID != "abc" {
		t.Error("PostID not propagated")
	}
	more := comments[1]
	if !more.More || more.Count != 42 || more.ParentCommentID() != "c9" {
		t.Errorf("unexpected more stub: %+v", more)
	}
}

func TestErrorStatus(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "banned", http.StatusNotFound)
	})
	if _, err := c.Posts(context.Background(), "nope", "hot", ListingOptions{}); err == nil {
		t.Fatal("expected error on 404")
	}
}
