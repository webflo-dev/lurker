// Package reddit is a small read-only client for Reddit's public JSON
// API, covering the endpoints lurker needs: listings, comment threads,
// subreddit info and search.
package reddit

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client talks to the Reddit JSON API.
type Client struct {
	// BaseURL of the API, default https://www.reddit.com. Overridable
	// for tests or alternative mirrors.
	BaseURL   string
	UserAgent string
	HTTP      *http.Client
}

// NewClient returns a Client with sane defaults.
func NewClient(userAgent string) *Client {
	return &Client{
		BaseURL:   "https://www.reddit.com",
		UserAgent: userAgent,
		HTTP:      &http.Client{Timeout: 15 * time.Second},
	}
}

// Post is a Reddit submission (kind t3).
type Post struct {
	ID            string  `json:"id"`
	Title         string  `json:"title"`
	Author        string  `json:"author"`
	Subreddit     string  `json:"subreddit"`
	Permalink     string  `json:"permalink"`
	URL           string  `json:"url"`
	Domain        string  `json:"domain"`
	Thumbnail     string  `json:"thumbnail"`
	SelftextHTML  string  `json:"selftext_html"`
	LinkFlairText string  `json:"link_flair_text"`
	PostHint      string  `json:"post_hint"`
	Score         int     `json:"score"`
	NumComments   int     `json:"num_comments"`
	CreatedUTC    float64 `json:"created_utc"`
	Over18        bool    `json:"over_18"`
	Spoiler       bool    `json:"spoiler"`
	Stickied      bool    `json:"stickied"`
	IsSelf        bool    `json:"is_self"`
	IsVideo       bool    `json:"is_video"`
	Media         *struct {
		RedditVideo *struct {
			FallbackURL string `json:"fallback_url"`
		} `json:"reddit_video"`
	} `json:"media"`
}

// HasThumbnail reports whether Thumbnail is a usable image URL (Reddit
// uses placeholder words like "self", "default" or "nsfw" otherwise).
func (p *Post) HasThumbnail() bool {
	return strings.HasPrefix(p.Thumbnail, "http")
}

// VideoURL returns the playable URL for v.redd.it posts, or "".
func (p *Post) VideoURL() string {
	if p.Media != nil && p.Media.RedditVideo != nil {
		return p.Media.RedditVideo.FallbackURL
	}
	return ""
}

// Comment is a node of a comment tree: either a real comment (kind t1)
// or, when More is true, a "load more comments" stub.
type Comment struct {
	ID            string  `json:"id"`
	Author        string  `json:"author"`
	BodyHTML      string  `json:"body_html"`
	Permalink     string  `json:"permalink"`
	Distinguished string  `json:"distinguished"`
	ParentID      string  `json:"parent_id"`
	Score         int     `json:"score"`
	ScoreHidden   bool    `json:"score_hidden"`
	IsSubmitter   bool    `json:"is_submitter"`
	Stickied      bool    `json:"stickied"`
	CreatedUTC    float64 `json:"created_utc"`

	// PostID of the submission this comment belongs to, filled in by
	// the client so templates can build links.
	PostID  string
	Replies []*Comment

	// More marks a stub representing Count collapsed replies under
	// ParentID.
	More  bool
	Count int
}

// Subreddit is the metadata of a community (kind t5).
type Subreddit struct {
	DisplayName       string  `json:"display_name"`
	Title             string  `json:"title"`
	PublicDescription string  `json:"public_description"`
	Subscribers       int     `json:"subscribers"`
	Over18            bool    `json:"over18"`
	CreatedUTC        float64 `json:"created_utc"`
}

// Listing is one page of posts.
type Listing struct {
	Posts []*Post
	// After is the pagination cursor for the next page, "" on the last.
	After string
}

// thing is Reddit's generic typed envelope.
type thing struct {
	Kind string          `json:"kind"`
	Data json.RawMessage `json:"data"`
}

type listingData struct {
	After    string  `json:"after"`
	Children []thing `json:"children"`
}

// ListingOptions controls pagination and time filtering.
type ListingOptions struct {
	After string
	// T is the time window for top/controversial sorts: hour, day,
	// week, month, year, all.
	T     string
	Limit int
}

func (o ListingOptions) values() url.Values {
	v := url.Values{}
	if o.After != "" {
		v.Set("after", o.After)
	}
	if o.T != "" {
		v.Set("t", o.T)
	}
	limit := o.Limit
	if limit == 0 {
		limit = 25
	}
	v.Set("limit", fmt.Sprint(limit))
	return v
}

// Posts fetches one page of a subreddit listing. subreddit may combine
// several names with "+".
func (c *Client) Posts(ctx context.Context, subreddit, sort string, opts ListingOptions) (*Listing, error) {
	var t thing
	path := fmt.Sprintf("/r/%s/%s.json", url.PathEscape(subreddit), sort)
	if err := c.get(ctx, path, opts.values(), &t); err != nil {
		return nil, err
	}
	return parseListing(t)
}

// About fetches a subreddit's metadata.
func (c *Client) About(ctx context.Context, subreddit string) (*Subreddit, error) {
	var t thing
	if err := c.get(ctx, fmt.Sprintf("/r/%s/about.json", url.PathEscape(subreddit)), nil, &t); err != nil {
		return nil, err
	}
	var sub Subreddit
	if err := json.Unmarshal(t.Data, &sub); err != nil {
		return nil, err
	}
	if sub.DisplayName == "" {
		return nil, fmt.Errorf("subreddit %q not found", subreddit)
	}
	return &sub, nil
}

// Comments fetches a submission and its comment tree.
func (c *Client) Comments(ctx context.Context, postID string) (*Post, []*Comment, error) {
	v := url.Values{"limit": {"100"}, "depth": {"8"}}
	return c.commentsRequest(ctx, postID, v)
}

// CommentThread fetches a submission with the comment tree rooted at a
// single comment (the "permalink" view).
func (c *Client) CommentThread(ctx context.Context, postID, commentID string) (*Post, []*Comment, error) {
	v := url.Values{"comment": {commentID}}
	return c.commentsRequest(ctx, postID, v)
}

func (c *Client) commentsRequest(ctx context.Context, postID string, v url.Values) (*Post, []*Comment, error) {
	var payload []json.RawMessage
	if err := c.get(ctx, fmt.Sprintf("/comments/%s.json", url.PathEscape(postID)), v, &payload); err != nil {
		return nil, nil, err
	}
	if len(payload) < 2 {
		return nil, nil, fmt.Errorf("unexpected comments response shape")
	}

	var postThing thing
	if err := json.Unmarshal(payload[0], &postThing); err != nil {
		return nil, nil, err
	}
	postListing, err := parseListing(postThing)
	if err != nil || len(postListing.Posts) == 0 {
		return nil, nil, fmt.Errorf("post %q not found", postID)
	}
	post := postListing.Posts[0]

	var commentsThing thing
	if err := json.Unmarshal(payload[1], &commentsThing); err != nil {
		return nil, nil, err
	}
	comments, err := parseComments(commentsThing)
	if err != nil {
		return nil, nil, err
	}
	setPostID(comments, post.ID)
	return post, comments, nil
}

// SearchPosts searches submissions site-wide.
func (c *Client) SearchPosts(ctx context.Context, query string, opts ListingOptions) (*Listing, error) {
	v := opts.values()
	v.Set("q", query)
	var t thing
	if err := c.get(ctx, "/search.json", v, &t); err != nil {
		return nil, err
	}
	return parseListing(t)
}

// SearchSubreddits searches communities by name and description.
func (c *Client) SearchSubreddits(ctx context.Context, query string) ([]*Subreddit, error) {
	v := url.Values{"q": {query}, "limit": {"25"}}
	var t thing
	if err := c.get(ctx, "/subreddits/search.json", v, &t); err != nil {
		return nil, err
	}
	var data listingData
	if err := json.Unmarshal(t.Data, &data); err != nil {
		return nil, err
	}
	var subs []*Subreddit
	for _, child := range data.Children {
		if child.Kind != "t5" {
			continue
		}
		var sub Subreddit
		if err := json.Unmarshal(child.Data, &sub); err != nil {
			return nil, err
		}
		subs = append(subs, &sub)
	}
	return subs, nil
}

func (c *Client) get(ctx context.Context, path string, query url.Values, out any) error {
	if query == nil {
		query = url.Values{}
	}
	// raw_json=1 disables Reddit's legacy HTML entity double-encoding.
	query.Set("raw_json", "1")
	u := c.BaseURL + path + "?" + query.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	// Reddit's CDN rejects requests that don't look like they come from
	// a browser, so mimic one beyond the User-Agent alone.
	req.Header.Set("User-Agent", c.UserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("Sec-Fetch-Dest", "document")
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	req.Header.Set("Sec-Fetch-Site", "none")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("reddit: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("reddit: %s returned %s", path, resp.Status)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("reddit: decoding %s: %w", path, err)
	}
	return nil
}

func parseListing(t thing) (*Listing, error) {
	var data listingData
	if err := json.Unmarshal(t.Data, &data); err != nil {
		return nil, err
	}
	listing := &Listing{After: data.After}
	for _, child := range data.Children {
		if child.Kind != "t3" {
			continue
		}
		var post Post
		if err := json.Unmarshal(child.Data, &post); err != nil {
			return nil, err
		}
		listing.Posts = append(listing.Posts, &post)
	}
	return listing, nil
}

// parseComments converts a comment Listing thing into a tree. Reddit
// encodes empty reply lists as the JSON string "" instead of an object,
// hence the lazy parsing of the replies field.
func parseComments(t thing) ([]*Comment, error) {
	var data listingData
	if err := json.Unmarshal(t.Data, &data); err != nil {
		return nil, err
	}
	var comments []*Comment
	for _, child := range data.Children {
		switch child.Kind {
		case "t1":
			var node struct {
				Comment
				RawReplies json.RawMessage `json:"replies"`
			}
			if err := json.Unmarshal(child.Data, &node); err != nil {
				return nil, err
			}
			comment := node.Comment
			if len(node.RawReplies) > 0 && node.RawReplies[0] == '{' {
				var repliesThing thing
				if err := json.Unmarshal(node.RawReplies, &repliesThing); err != nil {
					return nil, err
				}
				replies, err := parseComments(repliesThing)
				if err != nil {
					return nil, err
				}
				comment.Replies = replies
			}
			comments = append(comments, &comment)
		case "more":
			var more struct {
				Count    int    `json:"count"`
				ParentID string `json:"parent_id"`
			}
			if err := json.Unmarshal(child.Data, &more); err != nil {
				return nil, err
			}
			comments = append(comments, &Comment{
				More:     true,
				Count:    more.Count,
				ParentID: more.ParentID,
			})
		}
	}
	return comments, nil
}

func setPostID(comments []*Comment, postID string) {
	for _, c := range comments {
		c.PostID = postID
		setPostID(c.Replies, postID)
	}
}

// ParentCommentID strips the t1_ prefix from ParentID, for permalinks
// of "more" stubs.
func (c *Comment) ParentCommentID() string {
	return strings.TrimPrefix(c.ParentID, "t1_")
}
