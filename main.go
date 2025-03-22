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
	"github.com/urfave/cli/v2"
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
				return err
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
	baseURL := os.Getenv("PLEX_URL")
	token := os.Getenv("PLEX_TOKEN")

	if baseURL == "" || token == "" {
		logger.Error("PLEX_URL and PLEX_TOKEN environment variables must be set")
		os.Exit(1)
	}

	client, err := initialisePlexClient(baseURL, token)
	if err != nil {
		logger.Error("Error initialising client", "error", err)
		os.Exit(1)
	}

	logger.Debug("Client created successfully")

	logger.Debug("Creating output directory", "dir", outDir)
	dir := filepath.Dir(outDir)

	if _, err := os.Stat(dir); os.IsNotExist(err) {
		if err := os.MkdirAll(dir, 0755); err != nil {
			logger.Error("Error creating output directory", "error", err)
			os.Exit(1)
		}
	}

	// TODO - Make this pretty, add validation on input
	playlists, err := client.GetPlaylists()
	if err != nil {
		logger.Error("Error getting playlists", "error", err)
		return err
	}

	fmt.Println("Playlists:")
	for _, playlist := range playlists.MediaContainer.Metadata {
		fmt.Printf("%s: %s\n", playlist.RatingKey, playlist.Title)
	}

	var playlistID int
	fmt.Print("Enter playlist ID: ")
	fmt.Scan(&playlistID)

	tracks, err := downloadAndConvertTracks(client, playlistID, outDir)
	if err != nil {
		logger.Error("Error downloading tracks", "error", err)
		os.Exit(1)
	}

	createM3U(tracks, outDir)

	return nil
}

func initialisePlexClient(baseURL, token string) (*plex.Plex, error) {
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

func createM3U(tracks []Track, outDir string) error {
	m3uPath := filepath.Join(outDir, "plex-dj.m3u")
	dir := filepath.Dir(m3uPath)

	if _, err := os.Stat(dir); os.IsNotExist(err) {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("error creating directory %s: %v", dir, err)
		}
	}

	f, err := os.Create(m3uPath)
	if err != nil {
		return fmt.Errorf("error creating file %s: %v", m3uPath, err)
	}
	defer f.Close()

	f.WriteString("#EXTM3U\n")

	for _, track := range tracks {
		f.WriteString(fmt.Sprintf("#EXTINF:%d,%s\n%s\n", track.Duration, track.Title, track.Path))
	}

	logger.Info("Playlist created", "path", m3uPath)
	return nil
}
