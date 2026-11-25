package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/hashicorp/go-hclog"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/types/known/structpb"
)

func number(n int) *int { return &n }

// fakeAura serves the plugin routes, and refuses any other API key than "key"
type fakeAura struct {
	mu           sync.Mutex
	purposes     []string
	images       []auraImage
	notified     []string
	autoDownload []string
	wholeSet     []string
}

func newFakeAura(t *testing.T) (*fakeAura, *httptest.Server) {
	t.Helper()
	f := &fakeAura{}
	mux := http.NewServeMux()
	auth := func(w http.ResponseWriter, r *http.Request) bool {
		if r.Header.Get("X-Api-Key") != "key" {
			w.WriteHeader(http.StatusUnauthorized)
			return false
		}
		return true
	}
	mux.HandleFunc("GET /api/plugin/silo/status", func(w http.ResponseWriter, r *http.Request) {
		if auth(w, r) {
			_, _ = w.Write([]byte(`{"status":"success","data":{"app_version":"0.9.109"}}`))
		}
	})
	mux.HandleFunc("GET /api/plugin/silo/images", func(w http.ResponseWriter, r *http.Request) {
		if !auth(w, r) {
			return
		}
		f.mu.Lock()
		f.purposes = append(f.purposes, r.URL.Query().Get("purpose"))
		images := f.images
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": map[string]any{"images": images}})
	})
	mux.HandleFunc("GET /api/plugin/silo/download/{token}", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("notify") != "1" {
			_, _ = w.Write([]byte("image bytes"))
			return
		}
		f.mu.Lock()
		f.notified = append(f.notified, r.PathValue("token"))
		f.autoDownload = append(f.autoDownload, r.URL.Query().Get("autodownload"))
		f.wholeSet = append(f.wholeSet, r.URL.Query().Get("wholeset"))
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return f, server
}

func newTestServer(t *testing.T, auraURL string) *server {
	t.Helper()
	s := &server{aura: newAuraClient(), logger: hclog.NewNullLogger()}
	s.aura.setSettings(auraSettings{URL: auraURL, APIKey: "key"})
	return s
}

func settingsEntry(t *testing.T, values map[string]any) *pluginv1.ConfigureRequest {
	t.Helper()
	value, err := structpb.NewStruct(values)
	if err != nil {
		t.Fatal(err)
	}
	return &pluginv1.ConfigureRequest{Config: []*pluginv1.ConfigEntry{{Key: "settings", Value: value}}}
}

func ids(t *testing.T, values map[string]any) *structpb.Struct {
	t.Helper()
	s, err := structpb.NewStruct(values)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestParseSettings(t *testing.T) {
	settings, err := parseSettings(map[string]any{"aura_url": " http://aura:3000/ ", "api_key": " key ", "auto_download": true, "whole_set": true})
	if err != nil || settings.URL != "http://aura:3000" || settings.APIKey != "key" || !settings.AutoDownload || !settings.WholeSet {
		t.Errorf("parseSettings = %+v %v", settings, err)
	}
	for _, bad := range []string{"", "aura:3000", "ftp://aura", "http://"} {
		if _, err := parseSettings(map[string]any{"aura_url": bad}); err == nil {
			t.Errorf("parseSettings(%q) should fail", bad)
		}
	}
}

func TestConfigure(t *testing.T) {
	_, aura := newFakeAura(t)
	s := &server{aura: newAuraClient(), logger: hclog.NewNullLogger()}
	ctx := context.Background()

	if _, err := s.Configure(ctx, settingsEntry(t, map[string]any{"aura_url": aura.URL, "api_key": "wrong"})); err == nil {
		t.Error("a refused API key should fail")
	}
	if _, err := s.Configure(ctx, settingsEntry(t, map[string]any{"aura_url": aura.URL, "api_key": "key"})); err != nil {
		t.Fatal(err)
	}
	if s.aura.currentSettings().URL != aura.URL {
		t.Error("the settings should be kept")
	}
	// An address nothing answers at fails, like a URL that doesn't resolve
	for _, url := range []string{"http://127.0.0.1:1", "http://test.invalid"} {
		_, err := s.Configure(ctx, settingsEntry(t, map[string]any{"aura_url": url}))
		if err == nil || !strings.Contains(err.Error(), "can't reach aura") {
			t.Errorf("%s: Configure = %v, want can't reach aura", url, err)
		}
	}
	// The last good settings are kept
	if s.aura.currentSettings().URL != aura.URL {
		t.Error("failed settings shouldn't replace the working ones")
	}

	// A server that answers without the plugin routes fails, like an older aura or a page that isn't aura
	older := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(older.Close)
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("<html>not aura</html>"))
	}))
	t.Cleanup(page.Close)
	// What aura answers for a route it doesn't have
	routeNotFound := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"status":"error","data":null,"error":{"function":"aura/routing/base.NotFound","message":"Route Not Found"}}`))
	}))
	t.Cleanup(routeNotFound.Close)
	for name, url := range map[string]string{"404": older.URL, "not aura": page.URL, "aura without the routes": routeNotFound.URL} {
		_, err := s.Configure(ctx, settingsEntry(t, map[string]any{"aura_url": url, "api_key": "key"}))
		if err == nil || !strings.Contains(err.Error(), "update aura") {
			t.Errorf("%s: Configure = %v, want an error telling to update aura", name, err)
		}
	}
}

func TestGetMetadata(t *testing.T) {
	s := newTestServer(t, "http://aura")
	cases := []struct {
		itemType string
		ids      map[string]any
		want     string
	}{
		{"movie", map[string]any{"tmdb": "530915"}, "movie:530915"},
		{"series", map[string]any{"tmdb": "1399", "tvdb": "121361"}, "show:1399"},
		{"season", map[string]any{"tmdb": "1399"}, ""},
		{"movie", map[string]any{"imdb": "tt1"}, ""},
	}
	for _, c := range cases {
		resp, err := s.GetMetadata(context.Background(), &pluginv1.GetMetadataRequest{ItemType: c.itemType, ProviderIds: ids(t, c.ids)})
		if err != nil {
			t.Fatal(err)
		}
		if got := resp.GetItem().GetProviderId(); got != c.want {
			t.Errorf("%s %v = %q, want %q", c.itemType, c.ids, got, c.want)
		}
		if c.want != "" && resp.GetItem().GetProviderIds().AsMap()[providerID] != c.want {
			t.Errorf("%s should store its aura ID under %q", c.itemType, providerID)
		}
	}
}

func TestGetImages(t *testing.T) {
	f, aura := newFakeAura(t)
	f.images = []auraImage{
		{ID: "p1", Kind: "poster", Language: "English", SetRank: 1, Saved: true, Width: 2000, Height: 3000, PreviewURL: "https://mediux/p1?v=1&key=thumb", DownloadPath: "/api/plugin/silo/download/p1"},
		{ID: "b1", Kind: "backdrop", Language: "English", SetRank: 1, PreviewURL: "https://mediux/b1?v=1&key=thumb", DownloadPath: "/api/plugin/silo/download/b1"},
		{ID: "p2", Kind: "poster", Language: "Spanish", SetRank: 2, PreviewURL: "https://mediux/p2?v=1&key=thumb", DownloadPath: "/api/plugin/silo/download/p2"},
		{ID: "s1", Kind: "season_poster", SeasonNumber: number(1), SetRank: 1, PreviewURL: "https://mediux/s1", DownloadPath: "/api/plugin/silo/download/s1"},
		{ID: "s2", Kind: "season_poster", SeasonNumber: number(2), SetRank: 1, PreviewURL: "https://mediux/s2", DownloadPath: "/api/plugin/silo/download/s2"},
		{ID: "t1", Kind: "titlecard", SeasonNumber: number(1), EpisodeNumber: number(1), SetRank: 1, PreviewURL: "https://mediux/t1", DownloadPath: "/api/plugin/silo/download/t1"},
	}
	s := newTestServer(t, aura.URL)
	ctx := context.Background()

	resp, err := s.GetImages(ctx, &pluginv1.GetImagesRequest{ProviderId: "show:1", ItemType: "series", Language: "fr", ProviderIds: ids(t, map[string]any{"tmdb": "1"})})
	if err != nil {
		t.Fatal(err)
	}
	images := resp.GetImages()
	if len(images) != 3 {
		t.Fatalf("got %d images, want the 2 posters and the backdrop", len(images))
	}
	// For a refresh, posters take the requested language and backdrops are textless
	if images[0].GetKind() != "poster" || images[0].GetLanguage() != "fr" || images[1].GetLanguage() != "" || images[2].GetLanguage() != "fr" {
		t.Errorf("languages = %q %q %q", images[0].GetLanguage(), images[1].GetLanguage(), images[2].GetLanguage())
	}
	// The size is the image's own, or the nominal one when aura didn't say
	if images[0].GetWidth() != 2000 || images[0].GetHeight() != 3000 || images[1].GetWidth() != 3840 || images[1].GetHeight() != 2160 {
		t.Errorf("sizes = %dx%d %dx%d, want 2000x3000 3840x2160", images[0].GetWidth(), images[0].GetHeight(), images[1].GetWidth(), images[1].GetHeight())
	}
	// The rating falls with the rank
	r0 := images[0].GetMetadata().AsMap()["rating"].(float64)
	r1 := images[1].GetMetadata().AsMap()["rating"].(float64)
	r2 := images[2].GetMetadata().AsMap()["rating"].(float64)
	if r0 != 10 || !(r0 > r1 && r1 > r2) {
		t.Errorf("ratings = %v %v %v, want falling from 10", r0, r1, r2)
	}

	// A season only has its own posters
	season := int32(2)
	resp, err = s.GetImages(ctx, &pluginv1.GetImagesRequest{ProviderId: "show:1", ItemType: "series", SeasonNumber: &season, ProviderIds: ids(t, map[string]any{"tmdb": "1"})})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.GetImages()) != 1 || resp.GetImages()[0].GetSeasonNumber() != 2 || resp.GetImages()[0].GetKind() != "poster" {
		t.Errorf("season 2 images = %v", resp.GetImages())
	}

	// The image picker asks aura for the gallery, and posters keep their own language
	gallery := metadata.NewIncomingContext(ctx, metadata.Pairs(imageRequestPurposeHeader, "gallery"))
	resp, err = s.GetImages(gallery, &pluginv1.GetImagesRequest{ProviderId: "show:1", ItemType: "series", Language: "fr", ProviderIds: ids(t, map[string]any{"tmdb": "1"})})
	if err != nil {
		t.Fatal(err)
	}
	if resp.GetImages()[0].GetLanguage() != "en" || resp.GetImages()[2].GetLanguage() != "es" {
		t.Errorf("gallery languages = %q %q, want en es", resp.GetImages()[0].GetLanguage(), resp.GetImages()[2].GetLanguage())
	}

	// The refresh images were asked for once and kept, the gallery asked separately
	f.mu.Lock()
	purposes := f.purposes
	f.mu.Unlock()
	if strings.Join(purposes, ",") != "refresh,gallery" {
		t.Errorf("asked aura for %v, want [refresh gallery]", purposes)
	}
}

// Silo's image picker sends no purpose header and only the item's TMDB, TVDB and IMDb IDs, so it gets the gallery
func TestGetImagesFromThePicker(t *testing.T) {
	f, aura := newFakeAura(t)
	f.images = []auraImage{
		{ID: "p1", Kind: "poster", Language: "English", PreviewURL: "https://mediux/p1", DownloadPath: "/api/plugin/silo/download/p1"},
		{ID: "s1", Kind: "season_poster", SeasonNumber: number(1), Language: "English", PreviewURL: "https://mediux/s1", DownloadPath: "/api/plugin/silo/download/s1"},
	}
	s := newTestServer(t, aura.URL)
	ctx := context.Background()
	pickerIDs := ids(t, map[string]any{"tmdb": "1", "tvdb": "2", "imdb": "tt3"})

	resp, err := s.GetImages(ctx, &pluginv1.GetImagesRequest{ItemType: "series", Language: "fr", ProviderIds: pickerIDs})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.GetImages()) != 1 || resp.GetImages()[0].GetLanguage() != "en" {
		t.Errorf("picker images = %v, want the poster in its own language", resp.GetImages())
	}

	// The season picker asks for one season the same way
	season := int32(1)
	resp, err = s.GetImages(ctx, &pluginv1.GetImagesRequest{ItemType: "series", SeasonNumber: &season, ProviderIds: pickerIDs})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.GetImages()) != 1 || resp.GetImages()[0].GetSeasonNumber() != 1 {
		t.Errorf("season picker images = %v", resp.GetImages())
	}

	// A refresh has the aura ID, so it isn't the gallery even without the header
	if _, err := s.GetImages(ctx, &pluginv1.GetImagesRequest{ProviderId: "show:1", ItemType: "series", ProviderIds: pickerIDs}); err != nil {
		t.Fatal(err)
	}

	f.mu.Lock()
	purposes := f.purposes
	f.mu.Unlock()
	if strings.Join(purposes, ",") != "gallery,refresh" {
		t.Errorf("asked aura for %v, want [gallery refresh]", purposes)
	}
}

func TestGetSeasonsAndEpisodes(t *testing.T) {
	f, aura := newFakeAura(t)
	f.images = []auraImage{
		{ID: "s1-best", Kind: "season_poster", SeasonNumber: number(1), DownloadPath: "/d/s1-best"},
		{ID: "s1-other", Kind: "season_poster", SeasonNumber: number(1), DownloadPath: "/d/s1-other"},
		{ID: "s0", Kind: "season_poster", SeasonNumber: number(0), DownloadPath: "/d/s0"},
		{ID: "e1", Kind: "titlecard", SeasonNumber: number(1), EpisodeNumber: number(1), DownloadPath: "/d/e1"},
		{ID: "e1-other", Kind: "titlecard", SeasonNumber: number(1), EpisodeNumber: number(1), DownloadPath: "/d/e1-other"},
		{ID: "e2", Kind: "titlecard", SeasonNumber: number(1), EpisodeNumber: number(2), DownloadPath: "/d/e2"},
		{ID: "s2e1", Kind: "titlecard", SeasonNumber: number(2), EpisodeNumber: number(1), DownloadPath: "/d/s2e1"},
	}
	s := newTestServer(t, aura.URL)
	ctx := context.Background()

	seasons, err := s.GetSeasons(ctx, &pluginv1.GetSeasonsRequest{SeriesProviderId: "show:1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(seasons.GetSeasons()) != 2 {
		t.Fatalf("got %d seasons, want 2", len(seasons.GetSeasons()))
	}
	ref, _ := decodePath(seasons.GetSeasons()[0].GetPosterPath())
	if seasons.GetSeasons()[0].GetSeasonNumber() != 1 || ref.DownloadPath != "/d/s1-best" {
		t.Errorf("season 1 poster = %v %q, want the best ranked", seasons.GetSeasons()[0].GetSeasonNumber(), ref.DownloadPath)
	}

	episodes, err := s.GetEpisodes(ctx, &pluginv1.GetEpisodesRequest{SeriesProviderId: "show:1", SeasonNumber: 1})
	if err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for _, episode := range episodes.GetEpisodes() {
		ref, _ := decodePath(episode.GetStillPath())
		got = append(got, ref.DownloadPath)
	}
	if strings.Join(got, ",") != "/d/e1,/d/e2" {
		t.Errorf("season 1 title cards = %v, want the best ranked of each episode", got)
	}
}

func TestResolve(t *testing.T) {
	f, aura := newFakeAura(t)
	s := newTestServer(t, aura.URL)
	path, err := encodePath(auraImage{DownloadPath: "/api/plugin/silo/download/token", PreviewURL: "https://images.mediux.io/assets/a?v=1&key=thumb",
		OriginalURL: "https://images.mediux.io/assets/a?v=1"})
	if err != nil {
		t.Fatal(err)
	}
	// Silo downloads the original from MediUX, it can't download artwork from aura on the LAN during a refresh
	cases := map[string]string{
		"original": "https://images.mediux.io/assets/a?v=1",
		"card":     "https://images.mediux.io/assets/a?v=1&key=thumb",
		"large":    "https://images.mediux.io/assets/a?v=1&key=jpg",
		"":         "https://images.mediux.io/assets/a?v=1&key=jpg",
	}
	for variant, want := range cases {
		if got := s.resolve("aura://"+path, variant); got != want {
			t.Errorf("resolve %q = %q, want %q", variant, got, want)
		}
	}
	// The new MediUX links have no key=thumb, so aura sends the full size JPG
	current, err := encodePath(auraImage{DownloadPath: "/api/plugin/silo/download/current", PreviewURL: "https://img.mediux.io/assets/w300/a.avif",
		LargeURL: "https://img.mediux.io/assets/jpg/a.jpg", OriginalURL: "https://img.mediux.io/assets/original/a.png"})
	if err != nil {
		t.Fatal(err)
	}
	for variant, want := range map[string]string{"card": "https://img.mediux.io/assets/w300/a.avif", "large": "https://img.mediux.io/assets/jpg/a.jpg", "": "https://img.mediux.io/assets/jpg/a.jpg"} {
		if got := s.resolve("aura://"+current, variant); got != want {
			t.Errorf("resolve %q with a large URL = %q, want %q", variant, got, want)
		}
	}

	// Silo sends the path without the scheme
	if got := s.resolve(path, "original"); got != cases["original"] {
		t.Errorf("resolve without the scheme = %q", got)
	}
	if got := s.resolve("aura://not-a-path", "original"); got != "" {
		t.Errorf("resolve of a bad path = %q, want empty", got)
	}

	// aura was told the image was used, once, though Silo asked for the original twice
	deadline := time.Now().Add(2 * time.Second)
	for {
		f.mu.Lock()
		notified := len(f.notified)
		f.mu.Unlock()
		if notified >= 1 || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	f.mu.Lock()
	notified := f.notified
	f.mu.Unlock()
	if len(notified) != 1 || notified[0] != "token" {
		t.Errorf("aura was told %v, want [token] once", notified)
	}
	f.mu.Lock()
	autoDownload := f.autoDownload
	f.mu.Unlock()
	if len(autoDownload) != 1 || autoDownload[0] != "" {
		t.Errorf("autodownload = %v, want it left off", autoDownload)
	}
	f.mu.Lock()
	wholeSet := f.wholeSet
	f.mu.Unlock()
	if len(wholeSet) != 1 || wholeSet[0] != "" {
		t.Errorf("wholeset = %v, want it left off so aura saves only the image's type", wholeSet)
	}

	// With Auto Download and Apply Whole Set on, aura is asked to save the whole set with Auto Download
	s.aura.setSettings(auraSettings{URL: aura.URL, APIKey: "key", AutoDownload: true, WholeSet: true})
	other, _ := encodePath(auraImage{DownloadPath: "/api/plugin/silo/download/other", PreviewURL: "https://p", OriginalURL: "https://o"})
	s.resolve(other, "original")
	deadline = time.Now().Add(2 * time.Second)
	for {
		f.mu.Lock()
		n := len(f.autoDownload)
		f.mu.Unlock()
		if n >= 2 || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	f.mu.Lock()
	autoDownload = f.autoDownload
	f.mu.Unlock()
	if len(autoDownload) != 2 || autoDownload[1] != "1" {
		t.Errorf("autodownload = %v, want 1 for the second image", autoDownload)
	}
	f.mu.Lock()
	wholeSet = f.wholeSet
	f.mu.Unlock()
	if len(wholeSet) != 2 || wholeSet[1] != "1" {
		t.Errorf("wholeset = %v, want 1 for the second image", wholeSet)
	}

	// An older aura without original URLs is downloaded from directly
	older, _ := encodePath(auraImage{DownloadPath: "/api/plugin/silo/download/old", PreviewURL: "https://p"})
	if got := s.resolve(older, "original"); got != aura.URL+"/api/plugin/silo/download/old" {
		t.Errorf("resolve without an original URL = %q", got)
	}
}
