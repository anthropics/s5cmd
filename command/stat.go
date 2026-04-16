package command

import (
	"context"
	"errors"
	"fmt"

	"github.com/hashicorp/go-multierror"
	"github.com/urfave/cli/v2"

	"github.com/peak/s5cmd/v2/log"
	"github.com/peak/s5cmd/v2/log/stat"
	"github.com/peak/s5cmd/v2/storage"
	"github.com/peak/s5cmd/v2/storage/url"
	"github.com/peak/s5cmd/v2/strutil"
)

// statNotFoundExitCode is returned when at least one object was not found
// and no other class of error occurred, letting callers distinguish a clean
// miss from auth/network/validation failures, which exit 1.
const statNotFoundExitCode = 2

var statHelpTemplate = `Name:
	{{.HelpName}} - {{.Usage}}

Usage:
	{{.HelpName}} [options] source [source ...]

Options:
	{{range .VisibleFlags}}{{.}}
	{{end}}
Examples:
	1. Show metadata for a remote object
		 > s5cmd {{.HelpName}} s3://bucket/prefix/object

	2. Show metadata for multiple remote objects
		 > s5cmd {{.HelpName}} s3://bucket/object1 s3://bucket/object2

	3. Show metadata for a specific version of an object
		 > s5cmd {{.HelpName}} --version-id VERSION_ID s3://bucket/object
`

func NewStatCommand() *cli.Command {
	cmd := &cli.Command{
		Name:               "stat",
		HelpName:           "stat",
		Usage:              "show object metadata via HeadObject (no ListBucket required)",
		CustomHelpTemplate: statHelpTemplate,
		Flags: []cli.Flag{
			&cli.BoolFlag{
				Name:    "etag",
				Aliases: []string{"e"},
				Usage:   "show entity tag (ETag) in the output",
			},
			&cli.BoolFlag{
				Name:    "humanize",
				Aliases: []string{"H"},
				Usage:   "human-readable output for object sizes",
			},
			&cli.StringFlag{
				Name:  "version-id",
				Usage: "use the specified version of an object",
			},
			&cli.BoolFlag{
				Name:  "raw",
				Usage: "disable the wildcard operations, useful with filenames that contains glob characters",
			},
		},
		Before: func(c *cli.Context) error {
			err := validateStatCommand(c)
			if err != nil {
				printError(commandFromContext(c), c.Command.Name, err)
			}
			return err
		},
		Action: func(c *cli.Context) (err error) {
			defer stat.Collect(c.Command.FullName(), &err)()

			op := c.Command.Name
			fullCommand := commandFromContext(c)

			srcs := make([]*url.URL, 0, c.Args().Len())
			for _, arg := range c.Args().Slice() {
				src, err := url.New(arg,
					url.WithVersion(c.String("version-id")),
					url.WithRaw(c.Bool("raw")))
				if err != nil {
					printError(fullCommand, op, err)
					return err
				}
				srcs = append(srcs, src)
			}

			return Stat{
				srcs:        srcs,
				op:          op,
				fullCommand: fullCommand,

				showEtag: c.Bool("etag"),
				humanize: c.Bool("humanize"),

				storageOpts: NewStorageOpts(c),
			}.Run(c.Context)
		},
	}
	cmd.BashComplete = getBashCompleteFn(cmd, true, false)
	return cmd
}

// Stat holds stat operation flags and states.
type Stat struct {
	srcs        []*url.URL
	op          string
	fullCommand string

	// flags
	showEtag bool
	humanize bool

	storageOpts storage.Options
}

// Run retrieves and prints metadata for each source object.
func (s Stat) Run(ctx context.Context) error {
	var merror *multierror.Error
	allNotFound := true

	for _, src := range s.srcs {
		client, err := storage.NewRemoteClient(ctx, src, s.storageOpts)
		if err != nil {
			merror = multierror.Append(merror, err)
			printError(s.fullCommand, s.op, err)
			allNotFound = false
			continue
		}

		object, err := client.Stat(ctx, src)
		if err != nil {
			merror = multierror.Append(merror, err)
			printError(s.fullCommand, s.op, err)
			var nf *storage.ErrGivenObjectNotFound
			if !errors.As(err, &nf) {
				allNotFound = false
			}
			continue
		}

		msg := StatMessage{
			Object:        object,
			showEtag:      s.showEtag,
			showHumanized: s.humanize,
		}
		log.Info(msg)
	}

	if merror.ErrorOrNil() == nil {
		return nil
	}
	if allNotFound {
		return cli.Exit("", statNotFoundExitCode)
	}
	return merror
}

// StatMessage is a structure for logging stat results.
type StatMessage struct {
	Object *storage.Object `json:"object"`

	showEtag      bool
	showHumanized bool
}

// humanize is a helper function to humanize bytes.
func (s StatMessage) humanize() string {
	if s.showHumanized {
		return strutil.HumanizeBytes(s.Object.Size)
	}
	return fmt.Sprintf("%d", s.Object.Size)
}

// String returns the string representation of StatMessage.
func (s StatMessage) String() string {
	var etag string
	var listFormat = "%19s"

	if s.showEtag {
		etag = s.Object.Etag
		listFormat = listFormat + " %-38s"
	} else {
		listFormat = listFormat + " %-1s"
	}

	listFormat = listFormat + " %12s "
	if s.Object.URL.VersionID != "" {
		listFormat = listFormat + " %-50s %s"
	} else {
		listFormat = listFormat + " %s%s"
	}

	return fmt.Sprintf(
		listFormat,
		s.Object.ModTime.Format(dateFormat),
		etag,
		s.humanize(),
		s.Object.URL.String(),
		s.Object.URL.VersionID,
	)
}

// JSON returns the JSON representation of StatMessage.
func (s StatMessage) JSON() string {
	return s.Object.JSON()
}

func validateStatCommand(c *cli.Context) error {
	if c.Args().Len() < 1 {
		return fmt.Errorf("expected at least one argument")
	}

	if c.String("version-id") != "" && c.Args().Len() > 1 {
		return fmt.Errorf("version-id flag can only be used with a single source")
	}

	for _, arg := range c.Args().Slice() {
		src, err := url.New(arg,
			url.WithVersion(c.String("version-id")),
			url.WithRaw(c.Bool("raw")))
		if err != nil {
			return err
		}

		if !src.IsRemote() {
			return fmt.Errorf("source must be a remote object")
		}

		if src.IsBucket() || src.IsPrefix() {
			return fmt.Errorf("remote source must be an object")
		}

		if src.IsWildcard() {
			return fmt.Errorf("remote source %q can not contain glob characters", src)
		}
	}

	if err := checkVersioningWithGoogleEndpoint(c); err != nil {
		return err
	}

	return nil
}
