package photos

import (
	"context"
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	"github.com/int128/gpup/photos/internal"

	homedir "github.com/mitchellh/go-homedir"
	photoslibrary "google.golang.org/api/photoslibrary/v1"
)

const filedone string = "~/.gpupdone"

var uploadConcurrency = 4
var batchCreateSize = 8

// AddToLibrary adds the items to the library.
// This method tries uploading all items and ignores any error.
// If no item could be uploaded, this method returns an error.
func (p *Photos) AddToLibrary(ctx context.Context, uploadItems []UploadItem) []*AddResult {
	return p.add(ctx, uploadItems, photoslibrary.BatchCreateMediaItemsRequest{})
}

// AddToAlbum adds the items to the album.
// This method tries uploading all items and ignores any error.
// If no item could be uploaded, this method returns an error.
func (p *Photos) AddToAlbum(ctx context.Context, title string, uploadItems []UploadItem) ([]*AddResult, error) {
	log.Printf("Finding album %s", title)
	album, err := p.FindAlbumByTitle(ctx, title)
	if err != nil {
		return nil, fmt.Errorf("Could not list albums: %s", err)
	}
	if album == nil {
		log.Printf("Creating album %s", title)
		created, err := p.service.CreateAlbum(ctx, &photoslibrary.CreateAlbumRequest{
			Album: &photoslibrary.Album{Title: title},
		})
		if err != nil {
			return nil, fmt.Errorf("Could not create an album: %s", err)
		}
		album = created
	}
	return p.add(ctx, uploadItems, photoslibrary.BatchCreateMediaItemsRequest{
		AlbumId:       album.Id,
		AlbumPosition: &photoslibrary.AlbumPosition{Position: "LAST_IN_ALBUM"},
	}), nil
}

// CreateAlbum creates an album with the media items.
// This method tries uploading all items and ignores any error.
// If no item could be uploaded, this method returns an error.
func (p *Photos) CreateAlbum(ctx context.Context, title string, uploadItems []UploadItem) ([]*AddResult, error) {
	log.Printf("Creating album %s", title)
	album, err := p.service.CreateAlbum(ctx, &photoslibrary.CreateAlbumRequest{
		Album: &photoslibrary.Album{Title: title},
	})
	if err != nil {
		return nil, fmt.Errorf("Could not create an album: %s", err)
	}
	return p.add(ctx, uploadItems, photoslibrary.BatchCreateMediaItemsRequest{
		AlbumId:       album.Id,
		AlbumPosition: &photoslibrary.AlbumPosition{Position: "LAST_IN_ALBUM"},
	}), nil
}

// AddResult represents result of the add operation.
type AddResult struct {
	MediaItem *photoslibrary.MediaItem
	Error     error
}

func checkHourForInternet() {
	fmt.Println("check hours for internet...")
	if time.Now().Hour() >= 2 && time.Now().Hour() < 14 {
	} else {
		log.Println("not in the good timezone...sleep for", 600*time.Second)
		time.Sleep(600 * time.Second)
		checkHourForInternet()
	}
}

func (p *Photos) add(ctx context.Context, uploadItems []UploadItem, req photoslibrary.BatchCreateMediaItemsRequest) []*AddResult {
	uploadQueue := make(chan *uploadTask, len(uploadItems))
	var batchCreateTasks []*batchCreateTask
	for _, batch := range split(uploadItems, batchCreateSize) {
		var bt batchCreateTask
		batchCreateTasks = append(batchCreateTasks, &bt)
		bt.wg.Add(len(batch))
		for _, item := range batch {
			ut := &uploadTask{wg: &bt.wg, item: item}
			bt.uploadTasks = append(bt.uploadTasks, ut)
			uploadQueue <- ut
		}
	}
	close(uploadQueue)
	log.Printf("Queued %d item(s)", len(uploadQueue))

	for i := 0; i < uploadConcurrency; i++ {
		go func() {
			for ut := range uploadQueue {
				checkHourForInternet()
				ut.token, ut.err = p.service.Upload(ctx, ut.item)
				fmt.Println("step 1: thread Upload done for ", ut.item.String())
				ut.wg.Done()
			}
		}()
	}

	// Philippe Rinfret
	// write to file if upload ok.
	fdone, err := homedir.Expand(filedone)
	if err != nil {
		log.Fatalf("Could not expand %s: %s", filedone, err)
	}
	f, err := os.OpenFile(fdone, os.O_APPEND|os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		log.Fatalf("Could not open %s: %s", filedone, err)
	}
	defer f.Close()

	for _, bt := range batchCreateTasks {
		bt.wg.Wait()
		req.NewMediaItems = bt.toNewMediaItems()
		if len(req.NewMediaItems) > 0 {
			log.Printf("Adding %d item(s)", len(req.NewMediaItems))
			bt.res, bt.err = p.service.BatchCreate(ctx, &req)
		}
		m := bt.toNewMediaItemResultMap()
		for _, ut := range bt.uploadTasks {
			if mr, ok := m[ut.token]; ok {
				if mr.Status.Code != 0 {
					log.Printf("Intra batch status: %s (code=%d) \n", mr.Status.Message, mr.Status.Code)
				} else {
					f.WriteString(ut.item.String() + "\n")
					log.Println("Intra batch status: OK", ut.item.String())
				}
			}
		}
	}

	var results []*AddResult
	for _, bt := range batchCreateTasks {
		m := bt.toNewMediaItemResultMap()
		for _, ut := range bt.uploadTasks {
			var r AddResult
			results = append(results, &r)
			if bt.err != nil {
				r.Error = fmt.Errorf("Error while batch create: %s", bt.err)
			} else if ut.err != nil {
				r.Error = fmt.Errorf("Error while upload: %s", ut.err)
			} else if mr, ok := m[ut.token]; ok {
				if mr.Status.Code != 0 {
					r.Error = fmt.Errorf("%s (code=%d)", mr.Status.Message, mr.Status.Code)
				} else {
					r.MediaItem = mr.MediaItem
				}
			}
		}
	}
	return results
}

func split(items []UploadItem, n int) [][]UploadItem {
	var batch []UploadItem
	var batches [][]UploadItem
	for len(items) >= n {
		batch, items = items[:n], items[n:]
		batches = append(batches, batch)
	}
	if len(items) > 0 {
		batches = append(batches, items)
	}
	return batches
}

type batchCreateTask struct {
	uploadTasks []*uploadTask
	wg          sync.WaitGroup
	res         *photoslibrary.BatchCreateMediaItemsResponse
	err         error
}

func (bt *batchCreateTask) toNewMediaItems() []*photoslibrary.NewMediaItem {
	ret := make([]*photoslibrary.NewMediaItem, 0)
	for _, ut := range bt.uploadTasks {
		if ut.token != "" {
			ret = append(ret, &photoslibrary.NewMediaItem{
				SimpleMediaItem: &photoslibrary.SimpleMediaItem{UploadToken: string(ut.token)},
				Description:     ut.item.Name(),
			})
		}
	}
	return ret
}

func (bt *batchCreateTask) toNewMediaItemResultMap() map[internal.UploadToken]*photoslibrary.NewMediaItemResult {
	m := make(map[internal.UploadToken]*photoslibrary.NewMediaItemResult)
	if bt.res == nil {
		return m
	}
	for _, r := range bt.res.NewMediaItemResults {
		m[internal.UploadToken(r.UploadToken)] = r
	}
	return m
}

type uploadTask struct {
	wg    *sync.WaitGroup
	item  UploadItem
	token internal.UploadToken
	err   error
}
