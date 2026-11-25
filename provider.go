package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/imagevariant"
	"github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/runtimedefault"
	"github.com/hashicorp/go-hclog"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/types/known/structpb"
)

// providerID is the capability ID, Silo keeps the item's aura ID under it
const providerID = "aura"

// A Silo that marks image picker requests would send this header, current Silo doesn't
const imageRequestPurposeHeader = "silo-image-request-purpose"

// What the images are for, aura only offers saved and favorite creator images for a refresh
const (
	purposeRefresh = "refresh"
	purposeGallery = "gallery"
)

// Nominal sizes, for an image aura doesn't know the size of, the picker needs one
var nominalSizes = map[string][2]int32{
	"poster":   {1000, 1500},
	"backdrop": {3840, 2160},
	"still":    {3840, 2160},
}

type server struct {
	runtimedefault.Server
	pluginv1.UnimplementedMetadataProviderServer
	pluginv1.UnimplementedImageResolverServer

	manifest *pluginv1.PluginManifest
	aura     *auraClient
	logger   hclog.Logger

	notifiedMu sync.Mutex
	notified   map[string]time.Time
}

func (s *server) GetManifest(context.Context, *pluginv1.GetManifestRequest) (*pluginv1.GetManifestResponse, error) {
	return &pluginv1.GetManifestResponse{Manifest: s.manifest}, nil
}

// Configure takes the settings after checking aura answers with the plugin routes.
// Silo starts the plugin for each request, so aura being down only fails requests until it's back.
func (s *server) Configure(ctx context.Context, r *pluginv1.ConfigureRequest) (*pluginv1.ConfigureResponse, error) {
	values := map[string]any{}
	for _, entry := range r.GetConfig() {
		if entry.GetKey() == "settings" && entry.GetValue() != nil {
			values = entry.GetValue().AsMap()
		}
	}
	settings, err := parseSettings(values)
	if err != nil {
		return nil, err
	}
	version, err := s.aura.status(ctx, settings)
	switch {
	case errors.Is(err, errUnauthorized):
		return nil, errors.New("aura refused the API key, copy it again from aura's settings")
	case errors.Is(err, errUnreachable):
		return nil, fmt.Errorf("can't reach aura at %s, check the URL is the address Silo reaches aura at: %w", settings.URL, err)
	case err != nil:
		return nil, err
	default:
		s.logger.Info("connected to aura", "url", settings.URL, "aura_version", version)
	}
	s.aura.setSettings(settings)
	return &pluginv1.ConfigureResponse{}, nil
}

// itemIdentity returns the TMDB ID and aura item type for a Silo request
func itemIdentity(itemType string, ids *structpb.Struct, ownID string) (tmdbID, auraType string) {
	switch itemType {
	case "movie":
		auraType = "movie"
	case "series", "season", "episode":
		auraType = "show"
	default:
		return "", ""
	}
	if ids != nil {
		if id, ok := ids.AsMap()["tmdb"].(string); ok && id != "" {
			return id, auraType
		}
	}
	// The aura ID is "movie:<tmdb>" or "show:<tmdb>"
	if prefix, id, found := strings.Cut(ownID, ":"); found && prefix == auraType && id != "" {
		return id, auraType
	}
	return "", auraType
}

// purpose is gallery for the image picker, otherwise a refresh.
// Without the header, the picker is the request without an aura ID: it only sends the item's TMDB, TVDB and IMDb IDs,
// while a refresh has the aura ID from GetMetadata.
func purpose(ctx context.Context, ownID string) string {
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		if values := md.Get(imageRequestPurposeHeader); len(values) == 1 {
			if values[0] == purposeGallery {
				return purposeGallery
			}
			return purposeRefresh
		}
	}
	if ownID == "" {
		return purposeGallery
	}
	return purposeRefresh
}

func (s *server) Search(context.Context, *pluginv1.SearchMetadataRequest) (*pluginv1.SearchMetadataResponse, error) {
	return &pluginv1.SearchMetadataResponse{}, nil
}

// GetMetadata gives the item an aura ID, which Silo needs before it asks for images. aura adds no text
func (s *server) GetMetadata(_ context.Context, r *pluginv1.GetMetadataRequest) (*pluginv1.GetMetadataResponse, error) {
	tmdbID, auraType := itemIdentity(r.GetItemType(), r.GetProviderIds(), r.GetProviderId())
	if tmdbID == "" || (auraType == "show" && r.GetItemType() != "series") {
		return &pluginv1.GetMetadataResponse{}, nil
	}
	ownID := auraType + ":" + tmdbID
	ids, err := structpb.NewStruct(map[string]any{providerID: ownID, "tmdb": tmdbID})
	if err != nil {
		return nil, err
	}
	return &pluginv1.GetMetadataResponse{Item: &pluginv1.MetadataItem{ProviderId: ownID, ItemType: r.GetItemType(), ProviderIds: ids}}, nil
}

// GetImages gives posters and backdrops for a movie or show, or the posters of one season
func (s *server) GetImages(ctx context.Context, r *pluginv1.GetImagesRequest) (*pluginv1.GetImagesResponse, error) {
	tmdbID, auraType := itemIdentity(r.GetItemType(), r.GetProviderIds(), r.GetProviderId())
	if tmdbID == "" {
		return &pluginv1.GetImagesResponse{}, nil
	}
	forPurpose := purpose(ctx, r.GetProviderId())
	images, err := s.aura.images(ctx, tmdbID, auraType, forPurpose)
	if err != nil {
		return nil, err
	}

	var chosen []auraImage
	for _, image := range images {
		switch {
		case r.SeasonNumber != nil:
			if image.Kind == "season_poster" && image.SeasonNumber != nil && int32(*image.SeasonNumber) == r.GetSeasonNumber() {
				chosen = append(chosen, image)
			}
		case image.Kind == "poster" || image.Kind == "backdrop":
			chosen = append(chosen, image)
		}
	}

	out := &pluginv1.GetImagesResponse{}
	for i, image := range chosen {
		record, err := imageRecord(image, i, len(chosen), forPurpose, r.GetLanguage())
		if err != nil {
			return nil, err
		}
		out.Images = append(out.Images, record)
	}
	return out, nil
}

// imageRecord turns an aura image into a Silo image record.
// Silo picks the highest rated image of the first provider, so the rating falls with aura's rank.
func imageRecord(image auraImage, index, total int, forPurpose, requestLanguage string) (*pluginv1.ImageRecord, error) {
	kind := image.Kind
	if kind == "season_poster" {
		kind = "poster"
	}
	path, err := encodePath(image)
	if err != nil {
		return nil, err
	}
	size := nominalSizes[kind]
	if image.Width > 0 && image.Height > 0 {
		size = [2]int32{int32(image.Width), int32(image.Height)}
	}
	record := &pluginv1.ImageRecord{Kind: kind, Url: "aura://" + path, Width: size[0], Height: size[1]}

	// Posters have text, backdrops are textless.
	// For a refresh aura only offers images it wants used, so posters take the requested language and win Silo's language preference.
	language := languageCode(image.Language)
	includesText := kind == "poster"
	if kind == "poster" && forPurpose == purposeRefresh && requestLanguage != "" {
		language = requestLanguage
	}
	if kind == "backdrop" {
		language = ""
	}
	record.Language = language

	rating := 10.0
	if total > 1 {
		rating = 10 - 9*float64(index)/float64(total-1)
	}
	fields := map[string]any{"rating": rating, "includes_text": includesText, "set": image.SetTitle, "creator": image.Creator}
	if record.Metadata, err = structpb.NewStruct(fields); err != nil {
		return nil, err
	}
	if image.SeasonNumber != nil && image.Kind == "season_poster" {
		n := int32(*image.SeasonNumber)
		record.SeasonNumber = &n
	}
	return record, nil
}

// GetSeasons gives each season the best ranked season poster
func (s *server) GetSeasons(ctx context.Context, r *pluginv1.GetSeasonsRequest) (*pluginv1.GetSeasonsResponse, error) {
	tmdbID, _ := itemIdentity("series", r.GetProviderIds(), r.GetSeriesProviderId())
	if tmdbID == "" {
		return &pluginv1.GetSeasonsResponse{}, nil
	}
	images, err := s.aura.images(ctx, tmdbID, "show", purpose(ctx, r.GetSeriesProviderId()))
	if err != nil {
		return nil, err
	}
	out := &pluginv1.GetSeasonsResponse{}
	seen := map[int]bool{}
	for _, image := range images {
		if image.Kind != "season_poster" || image.SeasonNumber == nil || seen[*image.SeasonNumber] {
			continue
		}
		seen[*image.SeasonNumber] = true
		path, err := encodePath(image)
		if err != nil {
			return nil, err
		}
		out.Seasons = append(out.Seasons, &pluginv1.SeasonRecord{SeasonNumber: int32(*image.SeasonNumber), PosterPath: "aura://" + path})
	}
	return out, nil
}

// GetEpisodes gives each episode of the season the best ranked title card
func (s *server) GetEpisodes(ctx context.Context, r *pluginv1.GetEpisodesRequest) (*pluginv1.GetEpisodesResponse, error) {
	tmdbID, _ := itemIdentity("series", r.GetProviderIds(), r.GetSeriesProviderId())
	if tmdbID == "" {
		return &pluginv1.GetEpisodesResponse{}, nil
	}
	images, err := s.aura.images(ctx, tmdbID, "show", purpose(ctx, r.GetSeriesProviderId()))
	if err != nil {
		return nil, err
	}
	out := &pluginv1.GetEpisodesResponse{}
	seen := map[int]bool{}
	for _, image := range images {
		if image.Kind != "titlecard" || image.SeasonNumber == nil || image.EpisodeNumber == nil ||
			int32(*image.SeasonNumber) != r.GetSeasonNumber() || seen[*image.EpisodeNumber] {
			continue
		}
		seen[*image.EpisodeNumber] = true
		path, err := encodePath(image)
		if err != nil {
			return nil, err
		}
		out.Episodes = append(out.Episodes, &pluginv1.EpisodeRecord{
			SeasonNumber: int32(*image.SeasonNumber), EpisodeNumber: int32(*image.EpisodeNumber), StillPath: "aura://" + path,
		})
	}
	return out, nil
}

func (s *server) ResolveImageURL(_ context.Context, r *pluginv1.ResolveImageURLRequest) (*pluginv1.ResolveImageURLResponse, error) {
	return &pluginv1.ResolveImageURLResponse{Url: s.resolve(r.GetPath(), r.GetVariant())}, nil
}

func (s *server) ResolveImageURLs(_ context.Context, r *pluginv1.ResolveImageURLsRequest) (*pluginv1.ResolveImageURLsResponse, error) {
	urls := make(map[string]string, len(r.GetPaths()))
	for _, path := range r.GetPaths() {
		urls[path] = s.resolve(path, r.GetVariant())
	}
	return &pluginv1.ResolveImageURLsResponse{Urls: urls}, nil
}

// resolve gives Silo MediUX's original, telling aura it was used, and MediUX's preview for every other size.
// Silo only asks for the original when it downloads an image to use it.
func (s *server) resolve(path, variant string) string {
	ref, err := decodePath(path)
	if err != nil {
		s.logger.Warn("can't read an aura image path", "error", err)
		return ""
	}
	switch variant {
	case imagevariant.Original:
		// An older aura has no original URL, so Silo downloads it from aura, which only works for images applied by hand
		if ref.OriginalURL == "" {
			settings := s.aura.currentSettings()
			if settings.URL == "" {
				return ""
			}
			return settings.URL + ref.DownloadPath
		}
		s.notifyUsed(ref.DownloadPath)
		return ref.OriginalURL
	case imagevariant.Card:
		return ref.PreviewURL
	default:
		// Larger sizes use MediUX's full size JPG instead of the thumbnail
		if ref.LargeURL != "" {
			return ref.LargeURL
		}
		// An older aura has no large URL, its preview URLs end in key=thumb
		return strings.Replace(ref.PreviewURL, "key=thumb", "key=jpg", 1)
	}
}

// How long a used image isn't told to aura again, Silo asks for an original more than once while caching
const notifyInterval = 5 * time.Minute

// notifyUsed tells aura about a used image in the background, Silo shouldn't wait for aura
func (s *server) notifyUsed(downloadPath string) {
	s.notifiedMu.Lock()
	if last, ok := s.notified[downloadPath]; ok && time.Since(last) < notifyInterval {
		s.notifiedMu.Unlock()
		return
	}
	if s.notified == nil {
		s.notified = map[string]time.Time{}
	}
	s.notified[downloadPath] = time.Now()
	s.notifiedMu.Unlock()

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := s.aura.notifyUsed(ctx, downloadPath); err != nil {
			s.logger.Warn("couldn't tell aura an image was used", "error", err)
			s.notifiedMu.Lock()
			delete(s.notified, downloadPath)
			s.notifiedMu.Unlock()
		}
	}()
}

// imageRef is what an aura:// path holds
type imageRef struct {
	DownloadPath string `json:"d"`
	PreviewURL   string `json:"p"`
	LargeURL     string `json:"l,omitempty"`
	OriginalURL  string `json:"o,omitempty"`
}

func encodePath(image auraImage) (string, error) {
	raw, err := json.Marshal(imageRef{DownloadPath: image.DownloadPath, PreviewURL: image.PreviewURL, LargeURL: image.LargeURL, OriginalURL: image.OriginalURL})
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// decodePath reads an aura:// path, Silo sometimes sends it without the scheme
func decodePath(path string) (imageRef, error) {
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(path, "aura://"))
	if err != nil {
		return imageRef{}, fmt.Errorf("not an aura image path: %w", err)
	}
	var ref imageRef
	if err := json.Unmarshal(raw, &ref); err != nil || ref.DownloadPath == "" {
		return imageRef{}, errors.New("not an aura image path")
	}
	return ref, nil
}
