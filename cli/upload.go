package cli

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/int128/gpup/photos"
	homedir "github.com/mitchellh/go-homedir"
)

func (c *CLI) upload(ctx context.Context) error {
	if len(c.Paths) == 0 {
		return fmt.Errorf("Nothing to upload")
	}
	uploadItems, err := c.findUploadItems()
	if err != nil {
		return err
	}
	if len(uploadItems) == 0 {
		return fmt.Errorf("Nothing to upload in %s", strings.Join(c.Paths, ", "))
	}
	log.Printf("The following %d items will be uploaded:", len(uploadItems))
	for i, uploadItem := range uploadItems {
		fmt.Fprintf(os.Stderr, "#%d: %s\n", i+1, uploadItem)
	}

	client, err := c.newOAuth2Client(ctx)
	if err != nil {
		return err
	}
	service, err := photos.New(client)
	if err != nil {
		return err
	}
	var results []*photos.AddResult
	switch {
	case c.AlbumTitle != "":
		results, err = service.AddToAlbum(ctx, c.AlbumTitle, uploadItems)
	case c.NewAlbum != "":
		results, err = service.CreateAlbum(ctx, c.NewAlbum, uploadItems)
	default:
		results = service.AddToLibrary(ctx, uploadItems)
	}
	if err != nil {
		return err
	}
	for i, r := range results {
		if r.Error != nil {
			fmt.Printf("#%d: %s: %s\n", i+1, uploadItems[i], r.Error)
		} else {
			fmt.Printf("#%d: %s: OK\n", i+1, uploadItems[i])
		}
	}
	return nil
}

func (c *CLI) findUploadItems() ([]photos.UploadItem, error) {
	client := c.newHTTPClient()
	uploadItems := make([]photos.UploadItem, 0)

	m := c.loadMapFileDone()
	for _, arg := range c.Paths {
		switch {
		case strings.HasPrefix(arg, "http://") || strings.HasPrefix(arg, "https://"):
			r, err := http.NewRequest("GET", arg, nil)
			if err != nil {
				return nil, fmt.Errorf("Could not parse URL: %s", err)
			}
			if c.RequestBasicAuth != "" {
				kv := strings.SplitN(c.RequestBasicAuth, ":", 2)
				r.SetBasicAuth(kv[0], kv[1])
			}
			for _, header := range c.RequestHeaders {
				kv := strings.SplitN(header, ":", 2)
				r.Header.Add(strings.TrimSpace(kv[0]), strings.TrimSpace(kv[1]))
			}
			uploadItems = append(uploadItems, &photos.HTTPUploadItem{
				Client:  client,
				Request: r,
			})
		default:
			if err := filepath.Walk(arg, func(name string, info os.FileInfo, err error) error {
				switch {
				case err != nil:
					return err
				case info.Mode().IsRegular():
					if filepath.Ext(name) == ".MOV" || filepath.Ext(name) == ".tif" || filepath.Ext(name) == ".AAE" || filepath.Ext(name) == ".mpg" || filepath.Ext(name) == ".mp4" {
						// rien
					} else {
						if _, ok := m[name]; ok {
							log.Println("Fichier déjà sync ", name)
						} else {
							log.Println("Fichier ajouté ", name)
							uploadItems = append(uploadItems, photos.FileUploadItem(name))
						}
					}
					return nil
				default:
					return nil
				}
			}); err != nil {
				return nil, fmt.Errorf("Error while finding files in %s: %s", arg, err)
			}
		}
	}
	return uploadItems, nil
}

func (c *CLI) loadMapFileDone() map[string]int64 {

	var m map[string]int64
	m = make(map[string]int64)

	fdone, err := homedir.Expand(c.ListDoneName)
	if err != nil {
		log.Fatalf("Could not expand %s: %s", c.ListDoneName, err)
	}

	file, err := os.Open(fdone)
	if err != nil {
		log.Fatal(err)
	}
	defer file.Close()

	var i int64
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		m[scanner.Text()] = i
		i++
	}

	if err := scanner.Err(); err != nil {
		log.Fatal(err)
	}

	return m
}
