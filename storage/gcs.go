package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"cloud.google.com/go/storage"
	"google.golang.org/api/iterator"

	"github.com/peak/s5cmd/v2/log"
	"github.com/peak/s5cmd/v2/storage/url"
)

// GCS is a storage type which interacts with Google Cloud Storage using gRPC.
type GCS struct {
	client *storage.Client
	dryRun bool
	log    log.Logger
}

// NewGRPCClient creates a new GCS client with gRPC support.
func NewGRPCClient(ctx context.Context, opts Options) (*GCS, error) {
	// Create gRPC-enabled client
	client, err := storage.NewGRPCClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create gRPC client: %w", err)
	}

	return &GCS{
		client: client,
		dryRun: opts.DryRun,
		log:    log.New(opts.LogLevel),
	}, nil
}

// Close closes the GCS client connection.
func (g *GCS) Close() error {
	if g.client != nil {
		return g.client.Close()
	}
	return nil
}

// Stat returns the Object structure describing the object.
func (g *GCS) Stat(ctx context.Context, src *url.URL) (*Object, error) {
	bucket := g.client.Bucket(src.Bucket)
	obj := bucket.Object(src.Path)

	attrs, err := obj.Attrs(ctx)
	if err != nil {
		if errors.Is(err, storage.ErrObjectNotExist) {
			return nil, &ErrGivenObjectNotFound{ObjectAbsPath: src.Absolute()}
		}
		return nil, err
	}

	return g.convertAttrsToObject(src, attrs), nil
}

// List lists objects and directories/prefixes in the src.
func (g *GCS) List(ctx context.Context, src *url.URL, followSymlinks bool) <-chan *Object {
	ch := make(chan *Object)

	go func() {
		defer close(ch)

		bucket := g.client.Bucket(src.Bucket)
		query := &storage.Query{
			Prefix: src.Path,
		}

		// If the path ends with a delimiter, we want to list objects with that prefix
		if strings.HasSuffix(src.Path, "/") || src.Path == "" {
			query.Delimiter = "/"
		}

		it := bucket.Objects(ctx, query)
		for {
			attrs, err := it.Next()
			if err == iterator.Done {
				break
			}
			if err != nil {
				obj := &Object{
					URL: src,
					Err: err,
				}
				ch <- obj
				return
			}

			// Handle prefixes (directories)
			if attrs.Prefix != "" {
				objURL := src.Clone()
				objURL.Path = attrs.Prefix
				obj := &Object{
					URL:  objURL,
					Type: ObjectType{mode: os.ModeDir | 0755},
				}
				ch <- obj
				continue
			}

			// Handle regular objects
			objURL := src.Clone()
			objURL.Path = attrs.Name
			obj := g.convertAttrsToObject(objURL, attrs)
			ch <- obj
		}
	}()

	return ch
}

// Delete deletes the object at the given URL.
func (g *GCS) Delete(ctx context.Context, src *url.URL) error {
	if g.dryRun {
		return nil
	}

	bucket := g.client.Bucket(src.Bucket)
	obj := bucket.Object(src.Path)

	if err := obj.Delete(ctx); err != nil {
		if errors.Is(err, storage.ErrObjectNotExist) {
			return &ErrGivenObjectNotFound{ObjectAbsPath: src.Absolute()}
		}
		return err
	}

	return nil
}

// MultiDelete deletes multiple objects.
func (g *GCS) MultiDelete(ctx context.Context, urls <-chan *url.URL) <-chan *Object {
	ch := make(chan *Object)

	go func() {
		defer close(ch)

		for url := range urls {
			err := g.Delete(ctx, url)
			obj := &Object{
				URL: url,
				Err: err,
			}
			ch <- obj
		}
	}()

	return ch
}

// Copy copies an object from src to dst.
func (g *GCS) Copy(ctx context.Context, src, dst *url.URL, metadata Metadata) error {
	if g.dryRun {
		return nil
	}

	srcBucket := g.client.Bucket(src.Bucket)
	srcObj := srcBucket.Object(src.Path)

	dstBucket := g.client.Bucket(dst.Bucket)
	dstObj := dstBucket.Object(dst.Path)

	// Check if src and dst are in the same bucket for server-side copy
	if src.Bucket == dst.Bucket {
		// Server-side copy
		copier := dstObj.CopierFrom(srcObj)

		// Apply metadata if provided
		if metadata.ContentType != "" {
			copier.ContentType = metadata.ContentType
		}
		if metadata.CacheControl != "" {
			copier.CacheControl = metadata.CacheControl
		}
		if metadata.ContentEncoding != "" {
			copier.ContentEncoding = metadata.ContentEncoding
		}
		if metadata.ContentDisposition != "" {
			copier.ContentDisposition = metadata.ContentDisposition
		}
		if metadata.StorageClass != "" {
			copier.StorageClass = metadata.StorageClass
		}
		if metadata.UserDefined != nil {
			copier.Metadata = metadata.UserDefined
		}

		_, err := copier.Run(ctx)
		return err
	}

	// Cross-bucket copy: download then upload
	reader, err := srcObj.NewReader(ctx)
	if err != nil {
		return fmt.Errorf("failed to read source object: %w", err)
	}
	defer reader.Close()

	writer := dstObj.NewWriter(ctx)

	// Apply metadata
	if metadata.ContentType != "" {
		writer.ContentType = metadata.ContentType
	}
	if metadata.CacheControl != "" {
		writer.CacheControl = metadata.CacheControl
	}
	if metadata.ContentEncoding != "" {
		writer.ContentEncoding = metadata.ContentEncoding
	}
	if metadata.ContentDisposition != "" {
		writer.ContentDisposition = metadata.ContentDisposition
	}
	if metadata.StorageClass != "" {
		writer.StorageClass = metadata.StorageClass
	}
	if metadata.UserDefined != nil {
		writer.Metadata = metadata.UserDefined
	}

	if _, err := io.Copy(writer, reader); err != nil {
		writer.Close()
		return fmt.Errorf("failed to copy data: %w", err)
	}

	return writer.Close()
}

// convertAttrsToObject converts GCS ObjectAttrs to storage.Object.
func (g *GCS) convertAttrsToObject(u *url.URL, attrs *storage.ObjectAttrs) *Object {
	modTime := attrs.Updated
	return &Object{
		URL:          u,
		Etag:         attrs.Etag,
		ModTime:      &modTime,
		Type:         ObjectType{mode: 0644},
		Size:         attrs.Size,
		StorageClass: StorageClass(attrs.StorageClass),
	}
}
