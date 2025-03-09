package main

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

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

func initialisePlexClient() (*plex.Plex, error) {
	baseURL := "http://192.168.68.110:32400"
	token := os.Getenv("PLEX_TOKEN")
	client, err := plex.New(baseURL, token)
	if err != nil {
		logger.Error("Error creating client", "error", err)
		return nil, err
	}

	if _, err = client.Test(); err != nil {
		logger.Error("Error testing client", "error", err)
		return nil, err
	}

	return client, nil
}

func downloadAndConvertTracks(client *plex.Plex, playlistID int, outDir string) ([]Track, error) {
	plexPlaylist, err := client.GetPlaylist(playlistID)
	if err != nil {
		logger.Error("Error getting playlist", "error", err)
		return nil, err
	}

	var tracks []Track
	var wg sync.WaitGroup
	var m sync.Mutex

	// TODO: use error group
	for _, track := range plexPlaylist.MediaContainer.Metadata {
		wg.Add(1)
		go func(track plex.Metadata) {
			defer wg.Done()
			logger.Info("Downloading track", "title", track.Title)
			if err := client.Download(track, outDir, false, true); err != nil {
				logger.Error("Error downloading track", "error", err)
				return
			}
			logger.Info("Downloaded track", "title", track.Title)

			m.Lock()
			tracks = append(tracks, Track{
				Title:    track.Title,
				Duration: track.Duration,
				Path:     filepath.Join(outDir, filepath.Base(track.Media[0].Part[0].File)),
			})
			m.Unlock()
		}(track)
	}

	wg.Wait()

	// TODO: use error group
	for i := range tracks {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if strings.HasSuffix(tracks[i].Path, ".flac") {
				mp3Path, err := convertFlacToMP3(tracks[i].Path, "320k")
				if err != nil {
					logger.Error("Error converting FLAC to MP3", "error", err)
					return
				}

				m.Lock()
				tracks[i].Path = mp3Path
				m.Unlock()
			}
		}(i)
	}

	wg.Wait()

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
