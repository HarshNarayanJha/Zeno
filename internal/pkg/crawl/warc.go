package crawl

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/CorentinB/warc"
	"github.com/remeh/sizedwaitgroup"
	uuid "github.com/satori/go.uuid"
	"github.com/sirupsen/logrus"
)

// dumpResponseToFile is like httputil.DumpResponse but dumps the response directly
// to a file and return its path
func (c *Crawl) dumpResponseToFile(resp *http.Response) (string, error) {
	var err error

	// Generate a file on disk with a unique name
	UUID := uuid.NewV4()
	filePath := filepath.Join(c.JobPath, "temp", UUID.String()+".temp")
	file, err := os.OpenFile(filePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return "", err
	}
	defer file.Close()

	// Write the response to the file directly
	err = resp.Write(file)
	if err != nil {
		os.Remove(filePath)
		return "", err
	}

	return filePath, nil
}

func (c *Crawl) initWARCWriter() {
	var rotatorSettings = warc.NewRotatorSettings()
	var err error

	os.MkdirAll(path.Join(c.JobPath, "temp"), os.ModePerm)
	go c.tempFilesCleaner()

	rotatorSettings.OutputDirectory = path.Join(c.JobPath, "warcs")
	rotatorSettings.Compression = "GZIP"
	rotatorSettings.Prefix = c.WARCPrefix
	rotatorSettings.WarcinfoContent.Set("software", "Zeno")
	if len(c.WARCOperator) > 0 {
		rotatorSettings.WarcinfoContent.Set("operator", c.WARCOperator)
	}

	c.WARCWriter, c.WARCWriterFinish, err = rotatorSettings.NewWARCRotator()
	if err != nil {
		logrus.WithFields(logrus.Fields{
			"error": err,
		}).Fatal("Error when initialize WARC writer")
	}
}

func (c *Crawl) writeWARCFromConnection(req, resp *io.PipeReader, URL *url.URL) (err error) {
	defer c.WaitGroup.Done()

	var (
		batch      = warc.NewRecordBatch()
		recordChan = make(chan *warc.Record)
	)

	swg := sizedwaitgroup.New(2)

	go func() {
		defer swg.Done()

		// initialize the request record
		var requestRecord = warc.NewRecord()
		requestRecord.Header.Set("WARC-Type", "request")
		requestRecord.Header.Set("WARC-Target-URI", URL.String())
		requestRecord.Header.Set("Host", URL.Host)
		requestRecord.Header.Set("Content-Type", "application/http; msgtype=request")

		var buf bytes.Buffer
		_, err := io.Copy(&buf, req)
		if err != nil {
			panic(err)
		}

		requestRecord.Content = &buf

		recordChan <- requestRecord
	}()

	go func() {
		defer swg.Done()

		// initialize the response record
		var responseRecord = warc.NewRecord()
		responseRecord.Header.Set("WARC-Type", "response")
		responseRecord.Header.Set("WARC-Target-URI", URL.String())
		responseRecord.Header.Set("Host", URL.Host)
		responseRecord.Header.Set("Content-Type", "application/http; msgtype=response")

		var buf bytes.Buffer
		_, err := io.Copy(&buf, resp)
		if err != nil {
			panic(err)
		}

		responseRecord.Content = &buf

		recordChan <- responseRecord
	}()

	swg.Wait()

	for i := 0; i < 2; i++ {
		record := <-recordChan
		batch.Records = append(batch.Records, record)
	}

	c.WARCWriter <- batch

	return nil
}

func (c *Crawl) writeWARC(resp *http.Response) (string, error) {
	var batch = warc.NewRecordBatch()
	var requestDump []byte
	var responseDump []byte
	var responsePath string
	var err error

	// Initialize the response record
	var responseRecord = warc.NewRecord()
	responseRecord.Header.Set("WARC-Type", "response")
	responseRecord.Header.Set("WARC-Target-URI", url.QueryEscape(resp.Request.URL.String()))
	responseRecord.Header.Set("Content-Type", "application/http; msgtype=response")

	// If the Content-Length is unknown or if it is higher than 2MB, then
	// we process the response directly on disk to not risk maxing-out the RAM.
	// Else, we use the httputil.DumpResponse function to dump the response.
	if resp.ContentLength == -1 || resp.ContentLength > 2097152 {
		responsePath, err = c.dumpResponseToFile(resp)
		if err != nil {
			return responsePath, err
		}

		responseRecord.PayloadPath = responsePath
	} else {
		responseDump, err = httputil.DumpResponse(resp, true)
		if err != nil {
			return responsePath, err
		}

		responseRecord.Content = strings.NewReader(string(responseDump))
	}

	// Dump request
	requestDump, err = httputil.DumpRequestOut(resp.Request, true)
	if err != nil {
		os.Remove(responsePath)
		return responsePath, err
	}

	// Initialize the request record
	var requestRecord = warc.NewRecord()
	requestRecord.Header.Set("WARC-Type", "request")
	requestRecord.Header.Set("WARC-Target-URI", url.QueryEscape(resp.Request.URL.String()))
	requestRecord.Header.Set("Host", resp.Request.URL.Host)
	requestRecord.Header.Set("Content-Type", "application/http; msgtype=request")

	requestRecord.Content = strings.NewReader(string(requestDump))

	// Append records to the record batch
	batch.Records = append(batch.Records, responseRecord, requestRecord)

	// If we used a temporary file on disk, we create a "response channel"
	// that we fit in the batch, so the WARC writer is able to tell us when
	// the writing is done, so we can delete the temporary file safely
	if responsePath != "" {
		batch.Done = make(chan bool)
		c.WARCWriter <- batch
		<-batch.Done
	} else {
		c.WARCWriter <- batch
	}

	return responsePath, nil
}
