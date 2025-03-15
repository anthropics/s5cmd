package storage

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"net/http"
	urlpkg "net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/aws/client"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/endpoints"
	"github.com/aws/aws-sdk-go/aws/request"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/aws/aws-sdk-go/service/s3/s3iface"
	"github.com/aws/aws-sdk-go/service/s3/s3manager"
	"github.com/aws/aws-sdk-go/service/s3/s3manager/s3manageriface"

	"github.com/peak/s5cmd/v2/log"
	"github.com/peak/s5cmd/v2/storage/url"
)

var sentinelURL = urlpkg.URL{}

const (
	// deleteObjectsMax is the max allowed objects to be deleted on single HTTP
	// request.
	deleteObjectsMax = 1000

	// Amazon Accelerated Transfer endpoint
	transferAccelEndpoint = "s3-accelerate.amazonaws.com"

	// Google Cloud Storage endpoint
	gcsEndpoint = "storage.googleapis.com"

	// the key of the object metadata which is used to handle retry decision on NoSuchUpload error
	metadataKeyRetryID = "s5cmd-upload-retry-id"
)

// Re-used AWS sessions dramatically improve performance.
var globalSessionCache = &SessionCache{
	sessions: map[Options]*session.Session{},
}

// S3 is a storage type which interacts with S3API, DownloaderAPI and
// UploaderAPI.
type S3 struct {
	api                    s3iface.S3API
	downloader             s3manageriface.DownloaderAPI
	uploader               s3manageriface.UploaderAPI
	endpointURL            urlpkg.URL
	dryRun                 bool
	useListObjectsV1       bool
	noSuchUploadRetryCount int
	requestPayer           string
	customHeaders          map[string]string
}

func (s *S3) RequestPayer() *string {
	if s.requestPayer == "" {
		return nil
	}
	return &s.requestPayer
}

func parseEndpoint(endpoint string) (urlpkg.URL, error) {
	if endpoint == "" {
		return sentinelURL, nil
	}

	u, err := urlpkg.Parse(endpoint)
	if err != nil {
		return sentinelURL, fmt.Errorf("parse endpoint %q: %v", endpoint, err)
	}

	return *u, nil
}

// NewS3Storage creates new S3 session.
func newS3Storage(ctx context.Context, opts Options) (*S3, error) {
	endpointURL, err := parseEndpoint(opts.Endpoint)
	if err != nil {
		return nil, err
	}

	awsSession, err := globalSessionCache.newSession(ctx, opts)
	if err != nil {
		return nil, err
	}

	return &S3{
		api:                    s3.New(awsSession),
		downloader:             s3manager.NewDownloader(awsSession),
		uploader:               s3manager.NewUploader(awsSession),
		endpointURL:            endpointURL,
		dryRun:                 opts.DryRun,
		useListObjectsV1:       opts.UseListObjectsV1,
		requestPayer:           opts.RequestPayer,
		noSuchUploadRetryCount: opts.NoSuchUploadRetryCount,
		customHeaders:          opts.CustomHeaders,
	}, nil
}

// Stat retrieves metadata from S3 object without returning the object itself.
func (s *S3) Stat(ctx context.Context, url *url.URL) (*Object, error) {
	input := &s3.HeadObjectInput{
		Bucket:       aws.String(url.Bucket),
		Key:          aws.String(url.Path),
		RequestPayer: s.RequestPayer(),
	}
	if url.VersionID != "" {
		input.SetVersionId(url.VersionID)
	}

	output, err := s.api.HeadObjectWithContext(ctx, input)
	if err != nil {
		if errHasCode(err, "NotFound") {
			return nil, &ErrGivenObjectNotFound{ObjectAbsPath: url.Absolute()}
		}
		return nil, err
	}

	etag := aws.StringValue(output.ETag)
	mod := aws.TimeValue(output.LastModified)

	obj := &Object{
		URL:     url,
		Etag:    strings.Trim(etag, `"`),
		ModTime: &mod,
		Size:    aws.Int64Value(output.ContentLength),
	}

	if s.noSuchUploadRetryCount > 0 {
		if retryID, ok := output.Metadata[metadataKeyRetryID]; ok {
			obj.retryID = *retryID
		}
	}

	if output.StorageClass != nil {
		obj.StorageClass = StorageClass(*output.StorageClass)
	}
	return obj, nil
}

// List lists objects of a source. It uses the PathStyle calling convention.
func (s *S3) List(ctx context.Context, url *url.URL, _ bool) <-chan *Object {
	var (
		resultCh = make(chan *Object)
		bucket   = url.Bucket
		prefix   = url.Path
		isS3Dir  = url.IsS3Dir()
	)

	keyMarker := ""
	if isS3Dir && prefix != "" && !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}

	if s.useListObjectsV1 {
		go func() {
			defer close(resultCh)

			params := &s3.ListObjectsInput{
				Bucket:       aws.String(bucket),
				Prefix:       aws.String(prefix),
				RequestPayer: s.RequestPayer(),
			}

			if isS3Dir {
				params.Delimiter = aws.String("/")
			}

			err := s.api.ListObjectsPagesWithContext(
				ctx,
				params,
				s.listObjectsCallback(ctx, prefix, isS3Dir, url, resultCh),
			)

			if err != nil && err != ctx.Err() {
				resultCh <- &Object{Err: err}
			}
		}()
	} else {
		go func() {
			defer close(resultCh)

			for object := range s.listObjectsV2(ctx, url) {
				resultCh <- object
			}
		}()
	}

	return resultCh
}

func (s *S3) listObjectsV2(ctx context.Context, url *url.URL) <-chan *Object {
	var (
		resultCh = make(chan *Object)
		bucket   = url.Bucket
		prefix   = url.Path
		isS3Dir  = url.IsS3Dir()
	)

	if isS3Dir && prefix != "" && !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}

	go func() {
		defer close(resultCh)
		params := &s3.ListObjectsV2Input{
			Bucket:       aws.String(bucket),
			Prefix:       aws.String(prefix),
			RequestPayer: s.RequestPayer(),
		}

		if isS3Dir {
			params.Delimiter = aws.String("/")
		}

		err := s.api.ListObjectsV2PagesWithContext(
			ctx,
			params,
			s.listObjectsV2Callback(ctx, prefix, isS3Dir, url, resultCh),
		)

		if err != nil && err != ctx.Err() {
			resultCh <- &Object{Err: err}
		}
	}()

	return resultCh
}

func (s *S3) listObjectsV2Callback(
	ctx context.Context,
	rootPrefix string,
	isS3Dir bool,
	urlPrefix *url.URL,
	resultCh chan *Object,
) func(page *s3.ListObjectsV2Output, lastPage bool) bool {
	return func(page *s3.ListObjectsV2Output, lastPage bool) bool {
		return s.listObjectsGenericCallback(
			ctx,
			rootPrefix,
			isS3Dir,
			urlPrefix,
			resultCh,
			page.CommonPrefixes,
			page.Contents,
		)
	}
}

func (s *S3) listObjectsCallback(
	ctx context.Context,
	rootPrefix string,
	isS3Dir bool,
	urlPrefix *url.URL,
	resultCh chan *Object,
) func(page *s3.ListObjectsOutput, lastPage bool) bool {
	return func(page *s3.ListObjectsOutput, lastPage bool) bool {
		return s.listObjectsGenericCallback(
			ctx,
			rootPrefix,
			isS3Dir,
			urlPrefix,
			resultCh,
			page.CommonPrefixes,
			page.Contents,
		)
	}
}

func (s *S3) listObjectsGenericCallback(
	ctx context.Context,
	rootPrefix string,
	isS3Dir bool,
	urlPrefix *url.URL,
	resultCh chan *Object,
	commonPrefixes []*s3.CommonPrefix,
	contents []*s3.Object,
) bool {
	var (
		hasObject bool
		isRoot    = rootPrefix == ""
	)

	if isS3Dir {
		for _, p := range commonPrefixes {
			prefix := aws.StringValue(p.Prefix)
			if !isRoot && prefix == rootPrefix {
				continue
			}

			hasObject = true

			o := &Object{
				URL: urlPrefix.AppendPrefix(strings.TrimPrefix(prefix, rootPrefix)),
				Type: ObjectType{
					mode: os.ModeDir,
				},
			}
			select {
			case <-ctx.Done():
				return false
			case resultCh <- o:
			}
		}
	}

	for _, c := range contents {
		key := aws.StringValue(c.Key)
		if !isRoot && key == rootPrefix {
			continue
		}

		hasObject = true

		var appendKey string
		if rootPrefix == "" {
			appendKey = key
		} else {
			appendKey = strings.TrimPrefix(key, rootPrefix)
		}

		o := &Object{
			URL:          urlPrefix.Append(appendKey),
			Etag:         strings.Trim(aws.StringValue(c.ETag), `"`),
			ModTime:      aws.TimeValue(c.LastModified),
			Size:         aws.Int64Value(c.Size),
			StorageClass: StorageClass(aws.StringValue(c.StorageClass)),
		}

		select {
		case <-ctx.Done():
			return false
		case resultCh <- o:
		}
	}

	if !hasObject {
		resultCh <- &Object{Err: ErrNoObjectFound}
	}

	return true
}

// Copy copies S3 object to an S3 destination.
func (s *S3) Copy(
	ctx context.Context,
	from *url.URL,
	to *url.URL,
	metadata Metadata,
) error {
	if s.dryRun {
		return nil
	}

	bucket := to.Bucket
	acl := metadata.ACL

	if err := s.checkS3VersioningStatus(ctx, to); err != nil {
		return err
	}

	input := &s3.CopyObjectInput{
		Bucket:       aws.String(bucket),
		Key:          aws.String(to.Path),
		CopySource:   aws.String(from.CloudFormat()),
		RequestPayer: s.RequestPayer(),
	}

	if to.VersionID != "" {
		input.VersionId = aws.String(to.VersionID)
	}
	if from.VersionID != "" {
		input.CopySource = aws.String(fmt.Sprintf("%s?versionId=%s", from.CloudFormat(), from.VersionID))
	}

	metadata.setACL(input)
	if err := metadata.setEncryption(input); err != nil {
		return err
	}

	_, err := s.api.CopyObjectWithContext(ctx, input)
	return err
}

// Get gets an object from S3 destination and writes it to the io.Writer.
func (s *S3) Get(
	ctx context.Context,
	from *url.URL,
) (io.ReadCloser, error) {
	if s.dryRun {
		return io.NopCloser(strings.NewReader("")), nil
	}

	return s.open(ctx, from)
}

// Put puts the data read from reader to S3 destination.
func (s *S3) Put(
	ctx context.Context,
	reader io.Reader,
	to *url.URL,
	metadata Metadata,
	concurrency int,
	partSize int64,
) error {
	if s.dryRun {
		_, err := io.Copy(io.Discard, reader)
		return err
	}

	if err := s.checkS3VersioningStatus(ctx, to); err != nil {
		return err
	}

	uploader := s.uploader
	if concurrency != 0 || partSize != 0 {
		uploadAPIClient := uploader.S3
		uploader = s3manager.NewUploaderWithClient(uploadAPIClient, func(u *s3manager.Uploader) {
			if concurrency != 0 {
				u.Concurrency = concurrency
			}

			if partSize != 0 {
				u.PartSize = partSize
			}
		})
	}

	input := &s3manager.UploadInput{
		Bucket:       aws.String(to.Bucket),
		Key:          aws.String(to.Path),
		Body:         reader,
		RequestPayer: s.RequestPayer(),
	}

	if to.VersionID != "" {
		input.VersionId = aws.String(to.VersionID)
	}

	metadata.setACL(input)
	metadata.setContentType(input)
	metadata.setContentEncoding(input)
	metadata.setContentDisposition(input)
	metadata.setCacheControl(input)
	metadata.setStorageClass(input)
	metadata.setMeta(input)
	metadata.setExpires(input)

	if err := metadata.setEncryption(input); err != nil {
		return err
	}

	retryCount := s.noSuchUploadRetryCount
	if retryCount > 0 {
		retryable := s3manager.NewRetryableClient(uploader.S3)
		retryable.MaxNoSuchUploadRetries = retryCount
		uploader.S3 = retryable

		// we need to set a retry id in order to identify the upload again.
		// we need to add this meta to all bucket operations that belongs to the
		// retryable upload. if retryable client does not see this in head response
		// it cannot find the relevant upload id to retry.
		if input.Metadata == nil {
			input.Metadata = map[string]*string{}
		}
		input.Metadata[metadataKeyRetryID] = generateRetryID()
	}

	_, err := uploader.UploadWithContext(ctx, input)
	return err
}

func (s *S3) Delete(ctx context.Context, url *url.URL) error {
	if s.dryRun {
		return nil
	}

	input := &s3.DeleteObjectInput{
		Bucket:       aws.String(url.Bucket),
		Key:          aws.String(url.Path),
		RequestPayer: s.RequestPayer(),
	}

	if url.VersionID != "" {
		input.VersionId = aws.String(url.VersionID)
	}

	_, err := s.api.DeleteObjectWithContext(ctx, input)
	return err
}

func (s *S3) MultiDelete(ctx context.Context, urls <-chan *url.URL) <-chan *Object {
	resultCh := make(chan *Object)

	go func() {
		defer close(resultCh)

		chunkSize := 0
		processChunk := false
		// deletions can be chunked up to deleteObjectsMax (1000) objects
		objects := make([]*s3.ObjectIdentifier, 0, deleteObjectsMax)
		buckets := map[string][]*s3.ObjectIdentifier{}

		for item := range urls {
			chunkSize++
			isMaxChunkSize := chunkSize%deleteObjectsMax == 0

			key := item.Path
			bucket := item.Bucket
			objects := buckets[bucket]

			obj := &s3.ObjectIdentifier{
				Key: aws.String(key),
			}

			if item.VersionID != "" {
				obj.VersionId = aws.String(item.VersionID)
			}

			buckets[bucket] = append(objects, obj)

			select {
			case <-ctx.Done():
				resultCh <- &Object{Err: ctx.Err()}
				return
			default:
				processChunk = processChunk || isMaxChunkSize || item.VersionID != ""
			}
		}

		for bucket, objects := range buckets {
			if len(objects) < 1 || len(bucket) < 1 {
				continue
			}

			s.deleteObjects(ctx, resultCh, bucket, objects)
		}
	}()

	return resultCh
}

func (s *S3) deleteObjects(ctx context.Context, resultCh chan *Object, bucket string, objects []*s3.ObjectIdentifier) {
	chunks := split(objects, deleteObjectsMax)

	for _, chunk := range chunks {
		if s.dryRun {
			for _, obj := range chunk {
				o := &Object{URL: &url.URL{Bucket: bucket, Path: aws.StringValue(obj.Key), VersionID: aws.StringValue(obj.VersionId)}}
				resultCh <- o
			}
			continue
		}

		input := &s3.DeleteObjectsInput{
			Bucket: aws.String(bucket),
			Delete: &s3.Delete{
				Objects: chunk,
				Quiet:   aws.Bool(true),
			},
			RequestPayer: s.RequestPayer(),
		}

		output, err := s.api.DeleteObjectsWithContext(ctx, input)
		if err != nil {
			resultCh <- &Object{Err: err}
			continue
		}

		for _, d := range output.Deleted {
			o := &Object{
				URL: &url.URL{
					Bucket:    bucket,
					Path:      aws.StringValue(d.Key),
					VersionID: aws.StringValue(d.VersionId),
				},
			}
			resultCh <- o
		}

		for _, e := range output.Errors {
			o := &Object{
				URL: &url.URL{
					Bucket:    bucket,
					Path:      aws.StringValue(e.Key),
					VersionID: aws.StringValue(e.VersionId),
				},
				Err: fmt.Errorf(aws.StringValue(e.Message)),
			}
			resultCh <- o
		}
	}
}

// open is an adapter for GetObject S3 operation to make it mimic Get method
// (returns io.ReadCloser). It's not the same as Get because this doesn't
// support concurrency.
func (s *S3) open(ctx context.Context, url *url.URL) (io.ReadCloser, error) {
	input := &s3.GetObjectInput{
		Bucket:       aws.String(url.Bucket),
		Key:          aws.String(url.Path),
		RequestPayer: s.RequestPayer(),
	}

	// s.downloader.Download allocates an byte array according to data size.
	// This could cause the program to crash when downloading large files.
	// See for more https://github.com/peak/s5cmd/pull/61.
	// It could've been solved by pre-allocating the array (but it's not
	// possible since 'Content-Length' is not reliable),
	// or using io.ReadCloser as shown below.

	if url.VersionID != "" {
		input.VersionId = aws.String(url.VersionID)
	}

	output, err := s.api.GetObjectWithContext(ctx, input)
	if err != nil {
		return nil, err
	}
	return output.Body, nil
}

// CreateBucket creates an S3 bucket.
func (s *S3) CreateBucket(ctx context.Context, bucket string, location string) error {
	input := &s3.CreateBucketInput{
		Bucket: aws.String(bucket),
	}

	if location != "" {
		input.CreateBucketConfiguration = &s3.CreateBucketConfiguration{
			LocationConstraint: aws.String(location),
		}
	}

	_, err := s.api.CreateBucketWithContext(ctx, input)
	return err
}

// DeleteBucket deletes an S3 bucket.
func (s *S3) DeleteBucket(ctx context.Context, bucket string) error {
	input := &s3.DeleteBucketInput{
		Bucket: aws.String(bucket),
	}

	_, err := s.api.DeleteBucketWithContext(ctx, input)
	return err
}

// ListBuckets lists S3 buckets.
func (s *S3) ListBuckets(ctx context.Context) ([]*Bucket, error) {
	input := &s3.ListBucketsInput{}

	out, err := s.api.ListBucketsWithContext(ctx, input)
	if err != nil {
		return nil, err
	}

	buckets := make([]*Bucket, 0)
	for _, b := range out.Buckets {
		buckets = append(buckets, &Bucket{
			CreationDate: aws.TimeValue(b.CreationDate),
			Name:         aws.StringValue(b.Name),
		})
	}

	return buckets, nil
}

// Stream opens an Object and encodes it using the given SDK's EventStreamDecoder.
func (s *S3) Stream(ctx context.Context, from *url.URL, decoder EventStreamDecoder) ([]byte, error) {
	if s.dryRun {
		return nil, nil
	}

	result, err := decoder.Decode()
	if err != nil {
		return nil, err
	}

	return result, nil
}

// SelectRequest makes the AWS S3 Select request for object filtering
func (s *S3) SelectRequest(ctx context.Context, url *url.URL, expression string, compressionType string, inputSerialization string, outputSerialization string) (io.ReadCloser, error) {
	if s.dryRun {
		return io.NopCloser(strings.NewReader("")), nil
	}
	var compression string
	if compressionType != "" {
		compression = compressionType
	} else {
		compression = "NONE"
	}
	input := &s3.SelectObjectContentInput{
		Bucket:         aws.String(url.Bucket),
		Key:            aws.String(url.Path),
		Expression:     aws.String(expression),
		ExpressionType: aws.String("SQL"),
		RequestPayer:   s.RequestPayer(),
		InputSerialization: &s3.InputSerialization{
			CompressionType: aws.String(compression),
		},
		OutputSerialization: &s3.OutputSerialization{},
	}

	if url.VersionID != "" {
		input.VersionId = aws.String(url.VersionID)
	}

	switch strings.ToLower(inputSerialization) {
	case "json":
		input.InputSerialization.JSON = &s3.JSONInput{
			Type: aws.String("DOCUMENT"),
		}
	case "csv":
		input.InputSerialization.CSV = &s3.CSVInput{
			FileHeaderInfo: aws.String("USE"),
		}
	case "parquet":
		input.InputSerialization.Parquet = &s3.ParquetInput{}
	default:
		input.InputSerialization.CSV = &s3.CSVInput{
			FileHeaderInfo: aws.String("USE"),
		}
	}

	switch strings.ToLower(outputSerialization) {
	case "json":
		input.OutputSerialization.JSON = &s3.JSONOutput{}
	case "csv":
		input.OutputSerialization.CSV = &s3.CSVOutput{}
	default:
		input.OutputSerialization.CSV = &s3.CSVOutput{}
	}

	resp, err := s.api.SelectObjectContentWithContext(ctx, input)
	if err != nil {
		return nil, err
	}

	resultReader, writer := io.Pipe()
	go func() {
		defer writer.Close()
		defer resp.EventStream.Close()
		toContinue := true
		for toContinue {
			event, err := resp.EventStream.ReadEvent()
			if err != nil {
				if err == io.EOF {
					toContinue = false
					continue
				}
				writer.CloseWithError(fmt.Errorf("error reading event from selectobjectcontent: %v", err))
				return
			}
			switch v := event.(type) {
			case *s3.RecordsEvent:
				_, err := writer.Write(v.Payload)
				if err != nil {
					writer.CloseWithError(fmt.Errorf("error writing payload to writer: %v", err))
					return
				}
			case *s3.StatsEvent:
				continue
			case *s3.EndEvent:
				toContinue = false
				continue
			case *s3.ProgressEvent:
				continue
			case *s3.ContinuationEvent:
				continue
			default:
				continue
			}
		}
	}()
	return resultReader, nil
}

// check if we can write to the bucket, if not, return information on how to enable versioning.
func (s *S3) checkS3VersioningStatus(ctx context.Context, url *url.URL) error {
	// no need to check if this request is a copy or a get
	if url == nil {
		return nil
	}

	return nil
}

func (s *S3) updateBucketVersioning(ctx context.Context, bucket string, versioningStatus string) error {
	_, err := s.api.PutBucketVersioningWithContext(ctx, &s3.PutBucketVersioningInput{
		Bucket: aws.String(bucket),
		VersioningConfiguration: &s3.VersioningConfiguration{
			Status: aws.String(versioningStatus),
		},
	})
	return err
}

// GetBucketVersioning returnsversioning property of the bucket
func (s *S3) GetBucketVersioning(ctx context.Context, bucket string) (string, error) {
	output, err := s.api.GetBucketVersioningWithContext(ctx, &s3.GetBucketVersioningInput{
		Bucket: aws.String(bucket),
	})
	if err != nil || output.Status == nil {
		return "", err
	}

	return *output.Status, nil

}

type sdkLogger struct{}

func (l sdkLogger) Log(args ...interface{}) {
	msg := log.TraceMessage{
		Message: fmt.Sprint(args...),
	}
	log.Trace(msg)
}

// SessionCache holds session.Session according to s3Opts and it synchronizes
// access/modification.
type SessionCache struct {
	sync.Mutex
	sessions map[Options]*session.Session
}

// newSession initializes a new AWS session with region fallback and custom
// options.
func (sc *SessionCache) newSession(ctx context.Context, opts Options) (*session.Session, error) {
	sc.Lock()
	defer sc.Unlock()

	if sess, ok := sc.sessions[opts]; ok {
		return sess, nil
	}

	awsCfg := aws.NewConfig()

	if opts.NoSignRequest {
		// do not sign requests when making service API calls
		awsCfg = awsCfg.WithCredentials(credentials.AnonymousCredentials)
	} else if opts.CredentialFile != "" || opts.Profile != "" {
		awsCfg = awsCfg.WithCredentials(
			credentials.NewSharedCredentials(opts.CredentialFile, opts.Profile),
		)
	}

	endpointURL, err := parseEndpoint(opts.Endpoint)
	if err != nil {
		return nil, err
	}

	// use virtual-host-style if the endpoint is known to support it,
	// otherwise use the path-style approach.
	isVirtualHostStyle := isVirtualHostStyle(endpointURL)

	useAccelerate := supportsTransferAcceleration(endpointURL)
	// AWS SDK handles transfer acceleration automatically. Setting the
	// Endpoint to a transfer acceleration endpoint would cause bucket
	// operations fail.
	if useAccelerate {
		endpointURL = sentinelURL
	}

	var httpClient *http.Client
	if opts.NoVerifySSL {
		httpClient = insecureHTTPClient
	}
	if opts.AuthGoogleADC {
		httpClient, err = newGoogleAuthenticationClient(ctx, httpClient)
		if err != nil {
			return nil, err
		}
	}

	awsCfg = awsCfg.
		WithEndpoint(endpointURL.String()).
		WithS3ForcePathStyle(!isVirtualHostStyle).
		WithS3UseAccelerate(useAccelerate).
		WithHTTPClient(httpClient).
		// TODO WithLowerCaseHeaderMaps and WithDisableRestProtocolURICleaning options
		// are going to be unnecessary and unsupported in AWS-SDK version 2.
		// They should be removed during migration.
		WithLowerCaseHeaderMaps(true).
		// Disable URI cleaning to allow adjacent slashes to be used in S3 object keys.
		WithDisableRestProtocolURICleaning(true)

	if opts.LogLevel == log.LevelTrace {
		awsCfg = awsCfg.WithLogLevel(aws.LogDebug).
			WithLogger(sdkLogger{})
	}

	awsCfg.Retryer = newCustomRetryer(opts.MaxRetries, opts.RetryForbidden)

	useSharedConfig := session.SharedConfigEnable
	{
		// Reverse of what the SDK does: if AWS_SDK_LOAD_CONFIG is 0 (or a
		// falsy value) disable shared configs
		loadCfg := os.Getenv("AWS_SDK_LOAD_CONFIG")
		if loadCfg != "" {
			if enable, _ := strconv.ParseBool(loadCfg); !enable {
				useSharedConfig = session.SharedConfigDisable
			}
		}
	}

	sess, err := session.NewSessionWithOptions(
		session.Options{
			Config:            *awsCfg,
			SharedConfigState: useSharedConfig,
		},
	)
	if err != nil {
		return nil, err
	}

	// get region of the bucket and create session accordingly. if the region
	// is not provided, it means we want region-independent session
	// for operations such as listing buckets, making a new bucket etc.
	// only get bucket region when it is not specified.
	if opts.region != "" {
		sess.Config.Region = aws.String(opts.region)
	} else {
		if err := setSessionRegion(ctx, sess, opts.bucket); err != nil {
			return nil, err
		}
	}

	// Add any custom headers to all requests
	if len(opts.CustomHeaders) > 0 {
		sess.Handlers.Build.PushBack(func(r *request.Request) {
			for key, value := range opts.CustomHeaders {
				r.HTTPRequest.Header.Set(key, value)
			}
		})
	}

	sc.sessions[opts] = sess

	return sess, nil
}

func (sc *SessionCache) clear() {
	sc.Lock()
	defer sc.Unlock()
	sc.sessions = map[Options]*session.Session{}
}

func setSessionRegion(ctx context.Context, sess *session.Session, bucket string) error {
	region := aws.StringValue(sess.Config.Region)

	if region != "" {
		return nil
	}

	// set default region
	sess.Config.Region = aws.String(endpoints.UsEast1RegionID)

	if bucket == "" {
		return nil
	}

	// auto-detection
	region, err := s3manager.GetBucketRegion(ctx, sess, bucket, "", func(r *request.Request) {
		// s3manager.GetBucketRegion uses Path style addressing and
		// AnonymousCredentials by default, updating Request's Config to match
		// the session config.
		r.Config.S3ForcePathStyle = sess.Config.S3ForcePathStyle
		r.Config.Credentials = sess.Config.Credentials
	})
	if err != nil {
		if errHasCode(err, "NotFound") {
			return err
		}
		// don't deny any request to the service if region auto-fetching
		// receives an error. Delegate error handling to command execution.
		err = fmt.Errorf("session: fetching region failed: %v", err)
		msg := log.ErrorMessage{Err: err.Error()}
		log.Error(msg)
	} else {
		sess.Config.Region = aws.String(region)
	}

	return nil
}

// customRetryer wraps the SDK's built in DefaultRetryer adding additional
// error codes. Such as, retry for S3 InternalError code.
type customRetryer struct {
	client.DefaultRetryer
	retryForbidden bool
}

func newCustomRetryer(maxRetries int, retryForbidden bool) *customRetryer {
	return &customRetryer{
		DefaultRetryer: client.DefaultRetryer{
			NumMaxRetries: maxRetries,
		},
		retryForbidden: retryForbidden,
	}
}

// ShouldRetry overrides SDK's built in DefaultRetryer, adding custom retry
// logics that are not included in the SDK.
func (c *customRetryer) ShouldRetry(req *request.Request) bool {
	shouldRetry := errHasCode(req.Error, "InternalError") ||
		errHasCode(req.Error, "RequestTimeTooSkewed") ||
		errHasCode(req.Error, "SlowDown") ||
		strings.Contains(req.Error.Error(), "connection reset") ||
		strings.Contains(req.Error.Error(), "connection timed out") ||
		(c.retryForbidden && (errHasCode(req.Error, "Forbidden") || errHasCode(req.Error, "AccessDenied")))

	if !shouldRetry {
		shouldRetry = c.DefaultRetryer.ShouldRetry(req)
	}

	// Errors related to tokens
	if errHasCode(req.Error, "ExpiredToken") || errHasCode(req.Error, "ExpiredTokenException") || errHasCode(req.Error, "InvalidToken") {
		return false
	}

	if shouldRetry && req.Error != nil {
		err := fmt.Errorf("retryable error: %v", req.Error)
		msg := log.DebugMessage{Err: err.Error()}
		log.Debug(msg)
	}

	return shouldRetry
}

var insecureHTTPClient = &http.Client{
	Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		Proxy:           http.ProxyFromEnvironment,
	},
}

func supportsTransferAcceleration(endpoint urlpkg.URL) bool {
	return endpoint.Hostname() == transferAccelEndpoint
}

func IsGoogleEndpoint(endpoint urlpkg.URL) bool {
	return endpoint.Hostname() == gcsEndpoint
}

// isVirtualHostStyle reports whether the given endpoint supports S3 virtual
// host style bucket name resolving. If a custom S3 API compatible endpoint is
// given, resolve the bucketname from the URL path.
func isVirtualHostStyle(endpoint urlpkg.URL) bool {
	return endpoint == sentinelURL || supportsTransferAcceleration(endpoint) || IsGoogleEndpoint(endpoint)
}

func errHasCode(err error, code string) bool {
	if err == nil || code == "" {
		return false
	}

	var awsErr awserr.Error
	if errors.As(err, &awsErr) {
		if awsErr.Code() == code {
			return true
		}
	}

	var multiUploadErr s3manager.MultiUploadFailure
	if errors.As(err, &multiUploadErr) {
		return errHasCode(multiUploadErr.OrigErr(), code)
	}

	return false

}

// IsCancelationError reports whether given error is a storage related
// cancelation error.
func IsCancelationError(err error) bool {
	return errHasCode(err, request.CanceledErrorCode)
}

// generate a retry ID for this upload attempt
func generateRetryID() *string {
	num, _ := rand.Int(rand.Reader, big.NewInt(math.MaxInt64))
	return aws.String(num.String())
}

// EventStreamDecoder decodes a s3.Event with
// the given decoder.
type EventStreamDecoder interface {
	Decode() ([]byte, error)
}

type JSONDecoder struct {
	decoder *json.Decoder
}

func NewJSONDecoder(reader io.Reader) EventStreamDecoder {
	return &JSONDecoder{
		decoder: json.NewDecoder(reader),
	}
}

func (jd *JSONDecoder) Decode() ([]byte, error) {
	var val json.RawMessage
	err := jd.decoder.Decode(&val)
	if err != nil {
		return nil, err
	}
	return val, nil
}

type CsvDecoder struct {
	decoder   *csv.Reader
	delimiter string
}

func NewCsvDecoder(reader io.Reader) EventStreamDecoder {
	csvDecoder := &CsvDecoder{
		decoder:   csv.NewReader(reader),
		delimiter: ",",
	}
	// returned values from AWS has double quotes in it
	// so we enable lazy quotes
	csvDecoder.decoder.LazyQuotes = true
	return csvDecoder
}

func (cd *CsvDecoder) Decode() ([]byte, error) {
	res, err := cd.decoder.Read()
	if err != nil {
		return nil, err
	}

	result := []byte{}
	for i, str := range res {
		if i != len(res)-1 {
			str = fmt.Sprintf("%s%s", str, cd.delimiter)
		}
		result = append(result, []byte(str)...)
	}
	return result, nil
}