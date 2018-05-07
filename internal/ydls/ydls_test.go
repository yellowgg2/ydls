package ydls

// TODO: test close reader prematurely

import (
	"context"
	"encoding/xml"
	"io"
	"io/ioutil"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wader/ydls/internal/ffmpeg"
	"github.com/wader/ydls/internal/leaktest"
	"github.com/wader/ydls/internal/rss"
	"github.com/wader/ydls/internal/stringprioset"
	"github.com/wader/ydls/internal/timerange"
	"github.com/wader/ydls/internal/youtubedl"
)

var youtubeTestVideoURL = "https://www.youtube.com/watch?v=C0DPdy98e4c"
var youtubeLongTestVideoURL = "https://www.youtube.com/watch?v=z7VYVjR_nwE"
var soundcloudTestAudioURL = "https://soundcloud.com/timsweeney/thedrifter"
var soundcloudTestPlaylistURL = "https://soundcloud.com/mattheis/sets/kindred-phenomena"

var testNetwork = os.Getenv("TEST_NETWORK") != ""
var testYoutubeldl = os.Getenv("TEST_YOUTUBEDL") != ""
var testFfmpeg = os.Getenv("TEST_FFMPEG") != ""

func stringsContains(strings []string, s string) bool {
	for _, ss := range strings {
		if ss == s {
			return true
		}
	}

	return false
}

func ydlsFromEnv(t *testing.T) YDLS {
	ydls, err := NewFromFile(os.Getenv("CONFIG"))
	if err != nil {
		t.Fatalf("failed to read config: %s", err)
	}

	return ydls
}

func TestSafeFilename(t *testing.T) {
	for _, c := range []struct {
		s      string
		expect string
	}{
		{"aba", "aba"},
		{"a/a", "a_a"},
		{"a\\a", "a_a"},
	} {
		actual := safeFilename(c.s)
		if actual != c.expect {
			t.Errorf("%s, got %v expected %v", c.s, actual, c.expect)
		}
	}
}

func TestForceCodec(t *testing.T) {
	if !testNetwork || !testFfmpeg || !testYoutubeldl {
		t.Skip("TEST_NETWORK, TEST_FFMPEG, TEST_YOUTUBEDL env not set")
	}

	defer leaktest.Check(t)()

	ydls := ydlsFromEnv(t)
	const formatName = "mkv"
	mkvFormat, _ := ydls.Config.Formats.FindByName(formatName)
	forceCodecs := []string{"opus", "vp9"}

	// make sure codecs are not the perferred ones
	for _, s := range mkvFormat.Streams {
		for _, forceCodec := range forceCodecs {
			if c, ok := s.CodecNames.First(); ok && c == forceCodec {
				t.Errorf("test sanity check failed: codec already the preferred one")
				return
			}
		}
	}

	ctx, cancelFn := context.WithCancel(context.Background())

	dr, err := ydls.Download(ctx,
		DownloadOptions{
			MediaRawURL: youtubeTestVideoURL,
			Format:      &mkvFormat,
			Codecs:      forceCodecs,
		},
		nil)
	if err != nil {
		cancelFn()
		t.Errorf("%s: download failed: %s", youtubeTestVideoURL, err)
		return
	}

	pi, err := ffmpeg.Probe(ctx, ffmpeg.Reader{Reader: io.LimitReader(dr.Media, 10*1024*1024)}, nil, nil)
	dr.Media.Close()
	dr.Wait()
	cancelFn()
	if err != nil {
		t.Errorf("%s: probe failed: %s", youtubeTestVideoURL, err)
		return
	}

	if pi.FormatName() != "matroska" {
		t.Errorf("%s: force codec failed: found %s", youtubeTestVideoURL, pi)
		return
	}

	for i := 0; i < len(forceCodecs); i++ {
		if pi.Streams[i].CodecName != forceCodecs[i] {
			t.Errorf("%s: force codec failed: %s != %s", youtubeTestVideoURL, pi.Streams[i].CodecName, forceCodecs[i])
			return
		}
	}

}

func TestTimeRangeOption(t *testing.T) {
	if !testNetwork || !testFfmpeg || !testYoutubeldl {
		t.Skip("TEST_NETWORK, TEST_FFMPEG, TEST_YOUTUBEDL env not set")
	}

	defer leaktest.Check(t)()

	ydls := ydlsFromEnv(t)
	const formatName = "mkv"
	mkvFormat, _ := ydls.Config.Formats.FindByName(formatName)

	timeRange, timeRangeErr := timerange.NewTimeRangeFromString("10s-15s")
	if timeRangeErr != nil {
		t.Fatalf("failed to parse time range")
	}

	ctx, cancelFn := context.WithCancel(context.Background())

	dr, err := ydls.Download(ctx,
		DownloadOptions{
			MediaRawURL: youtubeTestVideoURL,
			Format:      &mkvFormat,
			TimeRange:   timeRange,
		},
		nil)
	if err != nil {
		cancelFn()
		t.Fatalf("%s: download failed: %s", youtubeTestVideoURL, err)
	}

	pi, err := ffmpeg.Probe(ctx, ffmpeg.Reader{Reader: io.LimitReader(dr.Media, 10*1024*1024)}, nil, nil)
	dr.Media.Close()
	dr.Wait()
	cancelFn()
	if err != nil {
		t.Errorf("%s: probe failed: %s", youtubeTestVideoURL, err)
		return
	}

	if pi.Duration() != timeRange.Duration() {
		t.Errorf("%s: probed duration not %v, got %v", youtubeTestVideoURL, timeRange.Duration(), pi.Duration())
		return
	}
}

func TestMissingMediaStream(t *testing.T) {
	if !testNetwork || !testFfmpeg || !testYoutubeldl {
		t.Skip("TEST_NETWORK, TEST_FFMPEG, TEST_YOUTUBEDL env not set")
	}

	defer leaktest.Check(t)()

	ydls := ydlsFromEnv(t)
	const formatName = "mkv"
	mkvFormat, _ := ydls.Config.Formats.FindByName(formatName)

	ctx, cancelFn := context.WithCancel(context.Background())

	_, err := ydls.Download(ctx,
		DownloadOptions{
			MediaRawURL: soundcloudTestAudioURL,
			Format:      &mkvFormat,
		},
		nil)
	cancelFn()
	if err == nil {
		t.Fatal("expected download to fail")
	}
}

func TestFindYDLFormat(t *testing.T) {
	ydlFormats := []youtubedl.Format{
		{FormatID: "1", Protocol: "http", ACodec: "mp3", VCodec: "h264", TBR: 1},
		{FormatID: "2", Protocol: "http", ACodec: "", VCodec: "h264", TBR: 2},
		{FormatID: "3", Protocol: "http", ACodec: "aac", VCodec: "", TBR: 3},
		{FormatID: "4", Protocol: "http", ACodec: "vorbis", VCodec: "vp8", TBR: 4},
		{FormatID: "5", Protocol: "http", ACodec: "opus", VCodec: "vp9", TBR: 5},
	}

	for i, c := range []struct {
		ydlFormats       []youtubedl.Format
		mediaType        mediaType
		codecs           stringprioset.Set
		expectedFormatID string
	}{
		{ydlFormats, MediaAudio, stringprioset.New([]string{"mp3"}), "1"},
		{ydlFormats, MediaAudio, stringprioset.New([]string{"aac"}), "3"},
		{ydlFormats, MediaVideo, stringprioset.New([]string{"h264"}), "2"},
		{ydlFormats, MediaVideo, stringprioset.New([]string{"h264"}), "2"},
		{ydlFormats, MediaAudio, stringprioset.New([]string{"vorbis"}), "4"},
		{ydlFormats, MediaVideo, stringprioset.New([]string{"vp8"}), "4"},
		{ydlFormats, MediaAudio, stringprioset.New([]string{"opus"}), "5"},
		{ydlFormats, MediaVideo, stringprioset.New([]string{"vp9"}), "5"},
	} {
		actualFormat, actualFormatFound := findYDLFormat(c.ydlFormats, c.mediaType, c.codecs)
		if actualFormatFound && actualFormat.FormatID != c.expectedFormatID {
			t.Errorf("%d: expected format %s, got %s", i, c.expectedFormatID, actualFormat)
		}
	}
}

func TestContextCloseProbe(t *testing.T) {
	if !testNetwork || !testFfmpeg || !testYoutubeldl {
		t.Skip("TEST_NETWORK, TEST_FFMPEG, TEST_YOUTUBEDL env not set")
	}

	defer leaktest.Check(t)()

	ydls := ydlsFromEnv(t)
	const formatName = "mkv"
	mkvFormat, _ := ydls.Config.Formats.FindByName(formatName)

	ctx, cancelFn := context.WithCancel(context.Background())

	wg := sync.WaitGroup{}
	wg.Add(1)
	go func() {
		time.Sleep(2 * time.Second)
		cancelFn()
		wg.Done()
	}()
	_, err := ydls.Download(ctx,
		DownloadOptions{
			MediaRawURL: youtubeLongTestVideoURL,
			Format:      &mkvFormat,
		},
		nil)
	if err == nil {
		t.Error("expected error while probe")
	}
	cancelFn()
	wg.Wait()
}

func TestContextCloseDownload(t *testing.T) {
	if !testNetwork || !testFfmpeg || !testYoutubeldl {
		t.Skip("TEST_NETWORK, TEST_FFMPEG, TEST_YOUTUBEDL env not set")
	}

	defer leaktest.Check(t)()

	ydls := ydlsFromEnv(t)
	const formatName = "mkv"
	mkvFormat, _ := ydls.Config.Formats.FindByName(formatName)

	ctx, cancelFn := context.WithCancel(context.Background())

	dr, err := ydls.Download(ctx,
		DownloadOptions{
			MediaRawURL: youtubeLongTestVideoURL,
			Format:      &mkvFormat,
		},
		nil)
	if err != nil {
		t.Error("expected no error while probe")
	}
	wg := sync.WaitGroup{}
	wg.Add(1)
	go func() {
		time.Sleep(1 * time.Second)
		cancelFn()
		wg.Done()
	}()
	io.Copy(ioutil.Discard, dr.Media)
	cancelFn()
	wg.Wait()
}

func TestRSS(t *testing.T) {
	if !testNetwork || !testFfmpeg || !testYoutubeldl {
		t.Skip("TEST_NETWORK, TEST_FFMPEG, TEST_YOUTUBEDL env not set")
	}

	defer leaktest.Check(t)()

	ydls := ydlsFromEnv(t)
	const formatName = "rss"
	rssFormat, _ := ydls.Config.Formats.FindByName(formatName)

	ctx, cancelFn := context.WithCancel(context.Background())

	dr, err := ydls.Download(ctx,
		DownloadOptions{
			MediaRawURL: soundcloudTestPlaylistURL,
			Format:      &rssFormat,
			BaseURL:     &url.URL{Scheme: "http", Host: "dummy"},
			Items:       2,
		},
		nil)
	if err != nil {
		cancelFn()
		t.Fatalf("%s: download failed: %s", youtubeTestVideoURL, err)
	}
	defer cancelFn()

	if dr.Filename != "" {
		t.Errorf("expected no filename, got %s", dr.Filename)
	}
	expectedMIMEType := "text/xml"
	if dr.MIMEType != expectedMIMEType {
		t.Errorf("expected mimetype %s, got %s", expectedMIMEType, dr.MIMEType)
	}

	rssRoot := rss.RSS{}
	decoder := xml.NewDecoder(dr.Media)
	decodeErr := decoder.Decode(&rssRoot)
	if decodeErr != nil {
		t.Errorf("failed to parse rss: %s", decodeErr)
		return
	}
	dr.Media.Close()
	dr.Wait()

	expectedTitle := "Kindred Phenomena"
	if rssRoot.Channel.Title != expectedTitle {
		t.Errorf("expected title \"%s\" got \"%s\"", expectedTitle, rssRoot.Channel.Title)
	}

	// TODO: description, not returned by youtube-dl?

	expectedItemsCount := 2
	if len(rssRoot.Channel.Items) != expectedItemsCount {
		t.Errorf("expected %d items got %d", expectedItemsCount, len(rssRoot.Channel.Items))
	}

	itemOne := rssRoot.Channel.Items[0]

	expectedItemTitle := "A1 Mattheis - Herds"
	if rssRoot.Channel.Items[0].Title != expectedItemTitle {
		t.Errorf("expected title \"%s\" got \"%s\"", expectedItemTitle, itemOne.Title)
	}

	expectedItemDescriptionPrefix := "Releasing my debut"
	if !strings.HasPrefix(rssRoot.Channel.Items[0].Description, expectedItemDescriptionPrefix) {
		t.Errorf("expected description prefix \"%s\" got \"%s\"", expectedItemDescriptionPrefix, itemOne.Description)
	}

	expectedItemGUID := "http://dummy/mp3/https://soundcloud.com/mattheis/sets/kindred-phenomena#293285002"
	if rssRoot.Channel.Items[0].GUID != expectedItemGUID {
		t.Errorf("expected guid \"%s\" got \"%s\"", expectedItemGUID, itemOne.GUID)
	}

	expectedItemURL := "http://dummy/media.mp3?format=mp3&url=https%3A%2F%2Fsoundcloud.com%2Fmattheis%2Fa1-mattheis-herds"
	if itemOne.Enclosure.URL != expectedItemURL {
		t.Errorf("expected enclousure url \"%s\" got \"%s\"", expectedItemURL, itemOne.Enclosure.URL)
	}

	expectedItemType := "audio/mpeg"
	if itemOne.Enclosure.Type != expectedItemType {
		t.Errorf("expected enclousure type \"%s\" got \"%s\"", expectedItemType, itemOne.Enclosure.Type)
	}
}

func TestRSSStructure(t *testing.T) {
	rawXML := `
<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0" xmlns:itunes="http://www.itunes.com/dtds/podcast-1.0.dtd">
  <channel>
    <item>
    </item>
  </channel>
</rss>
`
	rssRoot := rss.RSS{}
	decodeErr := xml.Unmarshal([]byte(rawXML), &rssRoot)
	if decodeErr != nil {
		t.Errorf("failed to parse rss: %s", decodeErr)
		return
	}

	expectedItemsCount := 1
	if len(rssRoot.Channel.Items) != expectedItemsCount {
		t.Errorf("expected %d items got %d", expectedItemsCount, len(rssRoot.Channel.Items))
	}
}
