package main

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/AlecAivazis/survey/v2"
	"github.com/jrudio/go-plex-client"
	"github.com/urfave/cli/v2"
	"golang.org/x/sync/errgroup"
)

var logger = slog.New(slog.NewJSONHandler(os.Stdout, nil))

type Playlist struct {
	Title          string
	Tracks         []Track
	PlexPlaylistID int
}

type Track struct {
	Title    string
	Path     string
	Duration int
}

func main() {

	app := &cli.App{
		Name:  "export",
		Usage: "Export a Plex playlist to an M3U file",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:     "out-dir",
				Usage:    "output directory for files and playlist(s)",
				Required: true,
			},
		},
		Action: func(c *cli.Context) error {
			err := export(c.String("out-dir"))
			if err != nil {
				logger.Error("Error exporting playlist", "error", err)
				return fmt.Errorf("error exporting playlist: %w", err)
			}

			return nil
		},
	}

	if err := app.Run(os.Args); err != nil {
		logger.Error("Error running app", "error", err)
		os.Exit(1)
	}
}

func export(outDir string) error {
	baseURL, token, err := getPlexConfig()

	client, err := initialisePlexClient(baseURL, token)
	if err != nil {
		return fmt.Errorf("error initialising client: %w", err)
	}

	logger.Debug("Client created successfully")

	logger.Debug("Creating output directory", "dir", outDir)

	if err := createDirectory(outDir); err != nil {
		return fmt.Errorf("error creating directory: %w", err)
	}

	playlistsResponse, err := client.GetPlaylists()
	if err != nil {
		return fmt.Errorf("error getting playlists: %w", err)
	}

	playlists := playlistsResponse.MediaContainer.Metadata
	playlistsToExport, err := promptForPlaylistSelection(playlists)
	if err != nil {
		return fmt.Errorf("error selecting playlists: %w", err)
	}

	for _, playlist := range playlistsToExport {
		logger.Info("Exporting playlist", "playlist_name", playlist.Title, "playlist_id", playlist.RatingKey)
		exportPlaylist(client, playlist, outDir)
	}

	// remove all flac files from outDir
	if err := filepath.Walk(outDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return fmt.Errorf("error walking path %s: %w", path, err)
		}

		if strings.HasSuffix(info.Name(), ".flac") {
			if err := os.Remove(path); err != nil {
				return fmt.Errorf("error removing flac file %s: %w", path, err)
			}
			logger.Info("Removed flac file", "path", path)
		}

		return nil
	}); err != nil {
		return fmt.Errorf("error walking output directory: %w", err)
	}

	logger.Info("Export completed successfully", "output_directory", outDir)
	return nil
}

func exportPlaylist(client *plex.Plex, playlist plex.Metadata, baseDir string) error {
	outDir := filepath.Join(baseDir, playlist.Title)

	if err := createDirectory(outDir); err != nil {
		return fmt.Errorf("error creating directory: %w", err)
	}

	playlistIDInt, err := strconv.Atoi(playlist.RatingKey)
	if err != nil {
		return fmt.Errorf("error converting playlist ID to int: %w", err)
	}

	tracks, err := downloadAndConvertTracks(client, playlistIDInt, outDir)
	if err != nil {
		return fmt.Errorf("error downloading and converting tracks: %w", err)
	}

	if err := createM3U(tracks, playlist.Title, outDir); err != nil {
		return fmt.Errorf("error creating M3U: %w", err)
	}

	return nil
}

func initialisePlexClient(baseURL, token string) (*plex.Plex, error) {
	client, err := plex.New(baseURL, token)
	if err != nil {
		return nil, fmt.Errorf("error creating plex client: %w", err)
	}

	if _, err = client.Test(); err != nil {
		return nil, fmt.Errorf("error testing plex client: %w", err)
	}

	return client, nil
}

func downloadAndConvertTracks(client *plex.Plex, playlistID int, outDir string) ([]Track, error) {
	plexPlaylist, err := client.GetPlaylist(playlistID)
	if err != nil {
		return nil, fmt.Errorf("error getting playlist: %w", err)
	}

	var tracks []Track
	var g errgroup.Group
	var m sync.Mutex

	for _, track := range plexPlaylist.MediaContainer.Metadata {
		track := track
		g.Go(func() error {
			if doesTrackExist(track, outDir) {
				logger.Info("Track already exists, skipping", "title", track.Title)
				return nil
			}

			logger.Info("Downloading track", "title", track.Title)
			if err := client.Download(track, outDir, false, true); err != nil {
				return fmt.Errorf("error downloading track %s: %w", track.Title, err)
			}
			logger.Info("Downloaded track", "title", track.Title)

			track := Track{
				Title:    track.Title,
				Duration: track.Media[0].Duration,
				Path:     filepath.Join(outDir, filepath.Base(track.Media[0].Part[0].File)),
			}

			if strings.HasSuffix(track.Path, ".flac") {
				mp3Path, err := convertFlacToMP3(track.Path, "320k")
				if err != nil {
					return fmt.Errorf("error converting flac to mp3: %w", err)
				}

				track.Path = mp3Path
			}

			m.Lock()
			tracks = append(tracks, track)
			m.Unlock()

			return nil
		})
	}

	if err := g.Wait(); err != nil {
		return nil, fmt.Errorf("error downloading and converting tracks: %w", err)
	}

	return tracks, nil
}

func convertFlacToMP3(flacPath, bitrate string) (string, error) {
	baseName := filepath.Base(flacPath)
	mp3Name := strings.Replace(baseName, ".flac", ".mp3", 1)
	mp3Path := filepath.Join(filepath.Dir(flacPath), mp3Name)

	if _, err := os.Stat(mp3Path); err == nil {
		fmt.Printf("MP3 already exists: %s\n", mp3Path)
		return mp3Path, nil
	}

	cmd := exec.Command(
		"ffmpeg",
		"-i", flacPath,
		"-ab", bitrate,
		"-map_metadata", "0",
		"-id3v2_version", "3",
		mp3Path,
	)

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("error converting %s: %w", flacPath, err)
	}

	if err := os.Remove(flacPath); err != nil {
		return "", fmt.Errorf("error removing flac file %s: %w", flacPath, err)
	}

	fmt.Printf("Converted: %s -> %s\n", flacPath, mp3Path)
	return mp3Path, nil
}

func createM3U(tracks []Track, playlistName, outDir string) error {
	playlistName = sanitizeFileName(playlistName)
	m3uPath := filepath.Join(outDir, fmt.Sprintf("%s.m3u", playlistName))
	dir := filepath.Dir(m3uPath)

	if err := createDirectory(dir); err != nil {
		return fmt.Errorf("error creating directory: %w", err)
	}

	f, err := os.Create(m3uPath)
	if err != nil {
		return fmt.Errorf("error creating file %s: %w", m3uPath, err)
	}
	defer f.Close()

	f.WriteString("#EXTM3U\n")

	for _, track := range tracks {
		f.WriteString(fmt.Sprintf("#EXTINF:%d,%s\n%s\n", track.Duration, track.Title, track.Path))
	}

	logger.Info("M3U created", "path", m3uPath)
	return nil
}

func promptForPlaylistSelection(playlists []plex.Metadata) ([]plex.Metadata, error) {
	playlistTitles := []string{}
	for _, playlist := range playlists {
		playlistTitles = append(playlistTitles, playlist.Title)
	}

	selectedPlaylistIndices := []int{}

	prompt := &survey.MultiSelect{
		Message: "Select playlists to export",
		Options: playlistTitles,
	}

	if err := survey.AskOne(prompt, &selectedPlaylistIndices); err != nil {
		return nil, fmt.Errorf("error selecting playlists: %w", err)
	}

	fmt.Println("Selected playlists:", selectedPlaylistIndices)

	playlistsToExport := []plex.Metadata{}

	for _, index := range selectedPlaylistIndices {
		playlistsToExport = append(playlistsToExport, playlists[index])
	}

	return playlistsToExport, nil
}

func createDirectory(dir string) error {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("error creating directory %s: %w", dir, err)
		}
	}

	return nil
}

func sanitizeFileName(name string) string {
	return strings.ReplaceAll(name, " ", "_")
}

func getPlexConfig() (string, string, error) {
	baseURL := os.Getenv("PLEX_URL")
	token := os.Getenv("PLEX_TOKEN")
	if baseURL == "" || token == "" {
		return "", "", fmt.Errorf("PLEX_URL and PLEX_TOKEN environment variables must be set")
	}
	return baseURL, token, nil
}

func doesTrackExist(track plex.Metadata, outDir string) bool {
	flacPath := filepath.Join(outDir, filepath.Base(track.Media[0].Part[0].File))
	baseName := filepath.Base(flacPath)
	mp3Name := strings.Replace(baseName, ".flac", ".mp3", 1)
	mp3Path := filepath.Join(filepath.Dir(flacPath), mp3Name)

	if _, err := os.Stat(mp3Path); err == nil {
		fmt.Printf("MP3 already exists: %s\n", mp3Path)
		return true
	}

	fmt.Printf("MP3 does not exist: %s\n", mp3Path)
	return false
}
