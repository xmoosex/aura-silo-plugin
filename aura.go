package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// How long an item's images from aura are kept, one refresh asks for them many times
const imagesCacheTTL = 2 * time.Minute

// auraImage is one image from aura's /api/plugin/silo/images
type auraImage struct {
	ID            string `json:"id"`
	Kind          string `json:"kind"` // poster, backdrop, season_poster or titlecard
	SeasonNumber  *int   `json:"season_number"`
	EpisodeNumber *int   `json:"episode_number"`
	Language      string `json:"language"`
	SetID         string `json:"set_id"`
	SetTitle      string `json:"set_title"`
	Creator       string `json:"creator"`
	SetRank       int    `json:"set_rank"`
	Saved         bool   `json:"saved"`
	Width         int    `json:"width"`  // 0 from an older aura or when MediUX didn't say
	Height        int    `json:"height"` //
	PreviewURL    string `json:"preview_url"`
	LargeURL      string `json:"large_url"` // The full size JPG, empty from an older aura
	OriginalURL   string `json:"original_url"`
	DownloadPath  string `json:"download_path"`
}

type auraResponse[T any] struct {
	Status string `json:"status"`
	Data   T      `json:"data"`
	Error  *struct {
		Message string `json:"message"`
		Help    string `json:"help"`
	} `json:"error"`
}

// errUnauthorized is aura refusing the API key
var errUnauthorized = errors.New("aura refused the API key")

// errNoPluginRoutes is a server without aura's Silo plugin routes, an older aura or not aura at all
var errNoPluginRoutes = errors.New("this server doesn't have aura's Silo plugin routes, update aura to a version with the Silo plugin")

// errUnreachable wraps an error from before aura answered
var errUnreachable = errors.New("aura isn't reachable")

type auraSettings struct {
	URL          string
	APIKey       string
	AutoDownload bool // Sets aura saves for the plugin get Auto Download on
	WholeSet     bool // aura saves the Download Defaults' types with a used image, otherwise only its type
}

type imagesCacheEntry struct {
	images  []auraImage
	fetched time.Time
}

type auraClient struct {
	http *http.Client

	mu       sync.RWMutex
	settings auraSettings
	cache    map[string]imagesCacheEntry
}

func newAuraClient() *auraClient {
	return &auraClient{http: &http.Client{Timeout: 20 * time.Second}, cache: map[string]imagesCacheEntry{}}
}

// parseSettings checks the plugin settings from Silo
func parseSettings(values map[string]any) (auraSettings, error) {
	raw, _ := values["aura_url"].(string)
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if raw == "" {
		return auraSettings{}, errors.New("aura URL is required")
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return auraSettings{}, fmt.Errorf("aura URL must be like http://host:3000, got %q", raw)
	}
	key, _ := values["api_key"].(string)
	autoDownload, _ := values["auto_download"].(bool)
	wholeSet, _ := values["whole_set"].(bool)
	return auraSettings{URL: raw, APIKey: strings.TrimSpace(key), AutoDownload: autoDownload, WholeSet: wholeSet}, nil
}

func (c *auraClient) setSettings(settings auraSettings) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.settings = settings
	c.cache = map[string]imagesCacheEntry{}
}

func (c *auraClient) currentSettings() auraSettings {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.settings
}

// get calls aura's API and decodes the data
func get[T any](ctx context.Context, c *auraClient, settings auraSettings, path string, query url.Values) (T, error) {
	var zero T
	if settings.URL == "" {
		return zero, errors.New("aura isn't configured")
	}
	target := settings.URL + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return zero, err
	}
	req.Header.Set("Accept", "application/json")
	if settings.APIKey != "" {
		req.Header.Set("X-Api-Key", settings.APIKey)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return zero, fmt.Errorf("%w: %w", errUnreachable, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return zero, fmt.Errorf("%w: reading the response: %w", errUnreachable, err)
	}
	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return zero, errUnauthorized
	case http.StatusNotFound, http.StatusMethodNotAllowed:
		return zero, errNoPluginRoutes
	}

	// aura always answers with its JSON, anything else isn't aura's Silo plugin routes
	var decoded auraResponse[T]
	if err := json.Unmarshal(body, &decoded); err != nil || decoded.Status == "" {
		return zero, errNoPluginRoutes
	}
	if decoded.Status != "success" {
		// aura answers a route it doesn't have with this error
		if decoded.Error != nil && decoded.Error.Message == "Route Not Found" {
			return zero, errNoPluginRoutes
		}
		if decoded.Error != nil && decoded.Error.Message != "" {
			return zero, fmt.Errorf("aura: %s", decoded.Error.Message)
		}
		return zero, fmt.Errorf("aura returned %d", resp.StatusCode)
	}
	return decoded.Data, nil
}

// status checks aura can be reached with the settings
func (c *auraClient) status(ctx context.Context, settings auraSettings) (string, error) {
	data, err := get[struct {
		AppVersion string `json:"app_version"`
	}](ctx, c, settings, "/api/plugin/silo/status", nil)
	return data.AppVersion, err
}

// images returns aura's images for a movie or show, for a refresh or the gallery
func (c *auraClient) images(ctx context.Context, tmdbID, itemType, purpose string) ([]auraImage, error) {
	key := tmdbID + "|" + itemType + "|" + purpose
	c.mu.RLock()
	entry, ok := c.cache[key]
	settings := c.settings
	c.mu.RUnlock()
	if ok && time.Since(entry.fetched) < imagesCacheTTL {
		return entry.images, nil
	}

	data, err := get[struct {
		Images []auraImage `json:"images"`
	}](ctx, c, settings, "/api/plugin/silo/images", url.Values{"tmdb_id": {tmdbID}, "item_type": {itemType}, "purpose": {purpose}})
	if err != nil {
		return nil, err
	}

	// An empty list isn't kept, aura may add a new item soon
	if len(data.Images) > 0 {
		c.mu.Lock()
		c.cache[key] = imagesCacheEntry{images: data.Images, fetched: time.Now()}
		c.mu.Unlock()
	}
	return data.Images, nil
}

// notifyUsed tells aura that Silo used an image, aura then saves the image's type from its set, or the whole set
func (c *auraClient) notifyUsed(ctx context.Context, downloadPath string) error {
	settings := c.currentSettings()
	if settings.URL == "" {
		return errors.New("aura isn't configured")
	}
	query := "?notify=1"
	if settings.AutoDownload {
		query += "&autodownload=1"
	}
	if settings.WholeSet {
		query += "&wholeset=1"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, settings.URL+downloadPath+query, nil)
	if err != nil {
		return err
	}
	if settings.APIKey != "" {
		req.Header.Set("X-Api-Key", settings.APIKey)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("reaching aura: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		return fmt.Errorf("aura returned %d", resp.StatusCode)
	}
	return nil
}
