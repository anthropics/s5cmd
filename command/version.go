package command

import (
	"fmt"

	"github.com/urfave/cli/v2"

	"github.com/anthropics/s5cmd/v2/version"
)

func NewVersionCommand() *cli.Command {
	return &cli.Command{
		Name:     "version",
		HelpName: "version",
		Usage:    "print version",
		Action: func(c *cli.Context) error {
			fmt.Println(version.GetHumanVersion())
			return nil
		},
	}
}
