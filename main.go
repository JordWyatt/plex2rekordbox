package main

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/jrudio/go-plex-client"
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

	outDir := "/tmp/plex-playlist-export-test"

	client, err := initialisePlexClient()
	if err != nil {
		logger.Error("Error initialising client", "error", err)
		os.Exit(1)
	}

	logger.Debug("Client created successfully")

	tracks, err := downloadAndConvertTracks(client, 27903, outDir)
	if err != nil {
		logger.Error("Error downloading tracks", "error", err)
		os.Exit(1)
	}

	createM3U(tracks, outDir)
}

func downloadAndConvertTracks(client *plex.Plex, playlistID int, outDir string) ([]Track, error) {

	plexPlaylist, err := client.GetPlaylist(playlistID)
	if err != nil {
		logger.Error("Error getting playlist", "error", err)
		return nil, err
	}

	tracks := make([]Track, 0)

	// TODO: parallelize
	for _, track := range plexPlaylist.MediaContainer.Metadata {
		logger.Info("Track", "title", track.Title)
		err := client.Download(track, outDir, false, true)
		if err != nil {
			logger.Error("Error downloading track", "error", err)
		}
		tracks = append(tracks, Track{
			Title:    track.Title,
			Duration: track.Duration,
			Path:     filepath.Join(outDir, filepath.Base(track.Media[0].Part[0].File)),
		})
	}

	// Convert FLAC files to MP3
	for i := range tracks {
		track := tracks[i]
		if strings.HasSuffix(track.Path, ".flac") {
			mp3Path, err := convertFlacToMP3(track.Path, "320k")
			if err != nil {
				logger.Error("Error converting FLAC to MP3", "error", err)
			}
			tracks[i].Path = mp3Path
		}
	}

	return tracks, nil
}

func initialisePlexClient() (*plex.Plex, error) {
	baseURL := "http://192.168.68.110:32400"
	token := os.Getenv("PLEX_TOKEN")
	client, err := plex.New(baseURL, token)
	if err != nil {
		logger.Error("Error creating client", "error", err)
		return nil, err
	}

	_, err = client.Test()
	if err != nil {
		logger.Error("Error testing client", "error", err)
		return nil, err
	}

	return client, nil
}

// Convert FLAC file to MP3 using ffmpeg
func convertFlacToMP3(flacPath, bitrate string) (string, error) {

	// Generate output path
	baseName := filepath.Base(flacPath)
	mp3Name := strings.Replace(baseName, ".flac", ".mp3", 1)
	mp3Path := filepath.Join(filepath.Dir(flacPath), mp3Name)

	// Skip if MP3 already exists
	if _, err := os.Stat(mp3Path); err == nil {
		fmt.Printf("MP3 already exists: %s\n", mp3Path)
		return mp3Path, nil
	}

	// Convert FLAC to MP3 using ffmpeg
	cmd := exec.Command(
		"ffmpeg",
		"-i", flacPath,
		"-ab", "320k",
		"-map_metadata", "0",
		"-id3v2_version", "3",
		mp3Path,
	)

	err := cmd.Run()
	if err != nil {
		return "", fmt.Errorf("error converting %s: %v", flacPath, err)
	}

	fmt.Printf("Converted: %s -> %s\n", flacPath, mp3Path)
	return mp3Path, nil
}

func createM3U(tracks []Track, outDir string) {
	m3uPath := filepath.Join(outDir, "plex-dj.m3u")
	f, err := os.Create(m3uPath)
	if err != nil {
		logger.Error("Error creating m3u file", "error", err)
		return
	}
	defer f.Close()

	f.WriteString("#EXTM3U\n")

	for _, track := range tracks {
		f.WriteString(fmt.Sprintf("#EXTINF:%d,%s\n%s\n", track.Duration, track.Title, track.Path))
	}

	logger.Info("Playlist created", "path", m3uPath)
}
