package command

import (
	"fmt"

	"github.com/urfave/cli/v2"

	"github.com/peak/s5cmd/v2/log"
	"github.com/peak/s5cmd/v2/log/stat"
	"github.com/peak/s5cmd/v2/progressbar"
	"github.com/peak/s5cmd/v2/storage"
	"github.com/peak/s5cmd/v2/storage/url"
)

var swfHelpTemplate = `Name:
	{{.HelpName}} - {{.Usage}}

Usage:
	{{.HelpName}} command [command options] [arguments...]

Commands:
	{{range .Commands}}{{.Name}}{{"\t"}}{{.Usage}}
	{{end}}
Options:
	{{range .VisibleFlags}}{{.}}
	{{end}}
`

var migrateHelpTemplate = `Name:
	{{.HelpName}} - {{.Usage}}

Usage:
	{{.HelpName}} [options] source

Options:
	{{range .VisibleFlags}}{{.}}
	{{end}}
Examples:
	01. Migrate local files to a GCS bucket path
		 > s5cmd {{.HelpName}} -d gs://bucket/path/to/dest/ /local/source/

	02. Migrate local files to an S3 bucket path
		 > s5cmd {{.HelpName}} -d s3://bucket/path/to/dest/ /local/source/

Notes:
	The destination path (-d) must include a path inside the bucket, not just the bucket root.
	For example, use "gs://bucket/models/v1/" instead of "gs://bucket/".
`

// NewSwfCommand creates a new swf command with subcommands.
func NewSwfCommand() *cli.Command {
	cmd := &cli.Command{
		Name:               "swf",
		HelpName:           "swf",
		Usage:              "storage workflow commands",
		CustomHelpTemplate: swfHelpTemplate,
		Subcommands: []*cli.Command{
			NewMigrateCommand(),
		},
	}
	return cmd
}

// NewMigrateCommand creates the migrate subcommand.
func NewMigrateCommand() *cli.Command {
	cmd := &cli.Command{
		Name:               "migrate",
		HelpName:           "migrate",
		Usage:              "migrate files to cloud storage",
		CustomHelpTemplate: migrateHelpTemplate,
		Flags:              NewMigrateCommandFlags(),
		Before: func(c *cli.Context) error {
			err := validateMigrateCommand(c)
			if err != nil {
				printError(commandFromContext(c), c.Command.Name, err)
			}
			return err
		},
		Action: func(c *cli.Context) (err error) {
			defer stat.Collect(c.Command.FullName(), &err)()

			return runMigrate(c)
		},
	}

	cmd.BashComplete = getBashCompleteFn(cmd, false, false)
	return cmd
}

// NewMigrateCommandFlags returns the flags for the migrate command.
func NewMigrateCommandFlags() []cli.Flag {
	migrateFlags := []cli.Flag{
		&cli.StringFlag{
			Name:     "dest-path",
			Aliases:  []string{"d"},
			Usage:    "destination path in cloud storage (must include a path inside the bucket, not just the bucket root)",
			Required: true,
		},
	}
	sharedFlags := NewSharedFlags()
	return append(migrateFlags, sharedFlags...)
}

// validateMigrateCommand validates the migrate command arguments.
func validateMigrateCommand(c *cli.Context) error {
	if c.Args().Len() < 1 {
		return fmt.Errorf("expected source argument")
	}

	destPath := c.String("dest-path")
	if destPath == "" {
		return fmt.Errorf("destination path (-d/--dest-path) is required")
	}

	// Parse the destination URL
	dsturl, err := url.New(destPath)
	if err != nil {
		return fmt.Errorf("invalid destination path: %v", err)
	}

	// Check if destination is a remote URL
	if !dsturl.IsRemote() {
		return fmt.Errorf("destination path must be a cloud storage URL (s3:// or gs://), got: %q", destPath)
	}

	// Check if destination is just a bucket root without a path
	if dsturl.IsBucket() {
		return fmt.Errorf(
			"destination path %q is a bucket root; you must specify a path inside the bucket "+
				"(e.g., %s://%s/your/path/) to avoid accidentally placing files in the bucket root",
			destPath, dsturl.Scheme, dsturl.Bucket,
		)
	}

	return nil
}

// runMigrate executes the migrate operation.
func runMigrate(c *cli.Context) error {
	fullCommand := commandFromContext(c)

	destPath := c.String("dest-path")
	dsturl, err := url.New(destPath)
	if err != nil {
		printError(fullCommand, c.Command.Name, err)
		return err
	}

	// Get source arguments
	sources := c.Args().Slice()

	// Log the migration operation
	msg := log.InfoMessage{
		Operation: "migrate",
		Message:   fmt.Sprintf("migrating %d source(s) to %s", len(sources), dsturl.String()),
	}
	log.Info(msg)

	// Build the copy command arguments
	// swf migrate -d dest source1 source2 ... -> cp source dest
	// For now, we only support single source
	if len(sources) != 1 {
		return fmt.Errorf("migrate currently supports exactly one source argument")
	}

	source := sources[0]
	srcurl, err := url.New(source)
	if err != nil {
		printError(fullCommand, c.Command.Name, err)
		return err
	}

	// Create a copy operation
	copy := &Copy{
		src:         srcurl,
		dst:         dsturl,
		op:          "migrate",
		fullCommand: fullCommand,
		// flags from shared flags
		followSymlinks:        !c.Bool("no-follow-symlinks"),
		storageClass:          storage.StorageClass(c.String("storage-class")),
		concurrency:           c.Int("concurrency"),
		partSize:              c.Int64("part-size") * megabytes,
		encryptionMethod:      c.String("sse"),
		encryptionKeyID:       c.String("sse-kms-key-id"),
		encryptionContext:     c.String("sse-kms-encryption-context"),
		acl:                   c.String("acl"),
		forceGlacierTransfer:  c.Bool("force-glacier-transfer"),
		ignoreGlacierWarnings: c.Bool("ignore-glacier-warnings"),
		exclude:               c.StringSlice("exclude"),
		include:               c.StringSlice("include"),
		cacheControl:          c.String("cache-control"),
		expires:               c.String("expires"),
		contentType:           c.String("content-type"),
		contentEncoding:       c.String("content-encoding"),
		contentDisposition:    c.String("content-disposition"),
		srcRegion:             c.String("source-region"),
		dstRegion:             c.String("destination-region"),
		storageOpts:           NewStorageOpts(c),
		progressbar:           &progressbar.NoOp{},
	}

	return copy.Run(c.Context)
}
