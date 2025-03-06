import argparse
import datetime
import os
import shutil
import subprocess
from pathlib import Path
from tempfile import mkdtemp

DEPENDENCIES = ["git", "go", "hyperfine", "truncate"]


def main(argv=None):
    parser = argparse.ArgumentParser(
        description="Compare performance of two different builds of s5cmd.",
        formatter_class=argparse.ArgumentDefaultsHelpFormatter,
    )

    parser.add_argument(
        "-d",
        "--debug",
        action="store_true",
        help=(
            "Enable debug mode to run a single hyperfine command with --show-output,"
            " without otherwise running benchmarking"
        ),
    )
    parser.add_argument(
        "-s",
        "--s5cmd",
        nargs=2,
        metavar=("OLD", "NEW"),
        default=("latest_release", "master"),
        help="References to old and new s5cmd versions, for `git checkout VERSION`.",
    )
    parser.add_argument(
        "-w",
        "--warmup",
        default="0",
        help="Number of program executions before the actual benchmark:",
    )
    parser.add_argument(
        "-r", "--runs", default="1", help="Number of runs to perform for each command"
    )
    parser.add_argument(
        "-o", "--output_file_name", default="summary.md", help="Name of the output file"
    )
    parser.add_argument(
        "-b", "--bucket", required=True, help="Name of the bucket in remote"
    )
    parser.add_argument(
        "--provider", choices=["s3", "gcs"], default="s3", 
        help="Cloud storage provider (s3 or gcs)"
    )
    parser.add_argument(
        "-l",
        "--local-path",
        help="specify a local path for temporary files to be loaded.",
    )
    parser.add_argument(
        "-p",
        "--prefix",
        default="s5cmd-benchmarks",
        help="Key prefix to be used while uploading to a specified bucket",
    )
    parser.add_argument(
        "-hf",
        "--hyperfine-extra-flags",
        default=" --show-output",
        help="hyperfine global extra flags. "
        "Write in between quotation marks "
        "and start with a space to avoid bugs.",
    )
    parser.add_argument(
        "-sf",
        "--s5cmd-extra-flags",
        help=(
            "s5cmd global extra flags. Write in between quotation marks"
            " and start with a space to avoid bugs."
        ),
    )

    check_dependencies()
    args = parser.parse_args(argv)
    cwd = os.getcwd()

    local_dir, dst_path = create_bench_dir(args.bucket, args.prefix, args.local_path, args.provider)
    if not args.debug:
        print("The created local&remote files will be deleted at the end of tests.")
        print(
            f"Hyperfine will execute s5cmd uploads {args.warmup} times to warmup,"
            f" and {args.runs} times for measurements."
        )
    old_s5cmd, new_s5cmd = build_s5cmd_exec(args.s5cmd[0], args.s5cmd[1])

    scenarios = [
        Scenario(
            name="small files",
            cwd=cwd,
            dst_path=dst_path,
            local_dir=local_dir,
            file_size="1M",
            file_count="10000",
            s5cmd_args=args.s5cmd_extra_flags,
            hyperfine_args={
                "runs": args.runs,
                "warmup": args.warmup,
                "extra_flags": args.hyperfine_extra_flags,
            },
        ),
        Scenario(
            name="large file",
            cwd=cwd,
            dst_path=dst_path,
            local_dir=local_dir,
            file_size="10G",
            file_count="1",
            s5cmd_args=args.s5cmd_extra_flags,
            hyperfine_args={
                "runs": args.runs,
                "warmup": args.warmup,
                "extra_flags": args.hyperfine_extra_flags,
            },
        ),
        Scenario(
            name="very large file",
            cwd=cwd,
            dst_path=dst_path,
            local_dir=local_dir,
            file_size="300G",
            file_count="1",
            s5cmd_args=args.s5cmd_extra_flags,
            hyperfine_args={
                "runs": "1",
                "warmup": "0",
                "extra_flags": args.hyperfine_extra_flags,
            },
        ),
    ]
    scenario_details = []
    for s in scenarios:
        scenario_details.append(f"| {s.name} | {s.file_size} | {s.file_count} |")

    if args.debug:
        print("Running debug only")
        scenario = scenarios[0]
        scenario.setup(args.output_file_name)
        s5cmd_cmds = scenario.get_s5cmd_commands(old_s5cmd, new_s5cmd)
        cmd = [
            "hyperfine",
            "--show-output",
            "--runs", "1",
            "--warmup", "0",
            "-n", old_s5cmd.descriptive_name,
            s5cmd_cmds["old_upload"],
        ]
        print(f"Running debug command: {' '.join(cmd)}")
        run_cmd(cmd)
        cleanup(local_dir, cwd)
        return

    for idx, scenario in enumerate(scenarios):
        if idx == 0:
            scenario.setup(
                args.output_file_name,
                initialize_bench=True,
                all_scenario_details=scenario_details,
            )
        else:
            scenario.setup(args.output_file_name)
        scenario.run(old_s5cmd, new_s5cmd)
        scenario.teardown()

    # append detailed_summary to output_file_name
    with open(os.path.join(cwd, "detailed_summary.md"), "r+") as f:
        detailed_summary = " ".join(f.readlines())
    with open(os.path.join(cwd, args.output_file_name), "a") as f:
        f.write(detailed_summary)

    cleanup(local_dir, cwd)


class S5cmd:
    def __init__(self, *, name, tag):
        self.name = name
        self.tag = tag
        self.descriptive_name = f"s5cmd-{name}-{tag}"
        self.path = Path.home() / "code" / "benchmark-s5cmd" / self.name / "s5cmd"
        self.path.parent.mkdir(parents=True, exist_ok=True)
        self._build()

    def _build(self):
        previous_rev = run_cmd(["git", "rev-parse", "HEAD"]).strip()
        run_cmd(["git", "checkout", self.tag])
        run_cmd(["go", "build", "-o", str(self.path)])
        run_cmd(["git", "checkout", previous_rev])


class Scenario:
    def __init__(
        self,
        name,
        cwd,
        file_size,
        file_count,
        s5cmd_args,
        hyperfine_args,
        local_dir,
        dst_path,
    ):
        self.all_scenario_details = None
        self.initialize_bench = False
        self.run_name = None
        self.name = name
        self.cwd = cwd
        self.file_size = file_size
        self.file_count = file_count
        self.s5cmd_args = ""
        if s5cmd_args is not None:
            self.s5cmd_args = s5cmd_args
        self.hyperfine_args = hyperfine_args
        self.local_dir = local_dir
        self.folder_dir = ""
        self.output_file_name = ""
        self.run_types = ["upload", "download", "remove"]
        self.dst_path = dst_path

    def setup(
        self, output_file_name, initialize_bench=False, all_scenario_details=None
    ):
        self.output_file_name = output_file_name
        self.initialize_bench = initialize_bench
        self.all_scenario_details = all_scenario_details
        if self.file_count:
            self.file_count = int(self.file_count)
            self.create_files()
        else:
            self.folder_dir = f"{self.local_dir}/"

    def create_files(self):
        # create subdirectory under local_dir named with a scenario name
        # create file_count files with each file_size size
        self.folder_dir = f'{self.local_dir}/{self.name.replace(" ", "-")}'

        os.mkdir(self.folder_dir)
        os.chdir(self.folder_dir)

        if self.file_count <= 0:
            raise ValueError(f"{self.file_count} cannot be negative.")
        else:
            for i in range(self.file_count):
                run_cmd(
                    ["truncate", "-s", f"{int(to_bytes(self.file_size))}", f"tmp{i}"]
                )

    def teardown(self):
        shutil.rmtree(self.folder_dir)

    def run(self, old_s5cmd, new_s5cmd):
        s5cmd_cmds = self.get_s5cmd_commands(old_s5cmd, new_s5cmd)
        for run in self.run_types:
            os.chdir(self.folder_dir)
            cmd = [
                "hyperfine",
                "--export-markdown",
                os.path.join(self.local_dir, "temp.md"),
                "-u",
                "second",
                "--runs",
                self.hyperfine_args["runs"],
                "--warmup",
                self.hyperfine_args["warmup"],
                "-n",
                old_s5cmd.descriptive_name,
                "-n",
                new_s5cmd.descriptive_name,
                self.hyperfine_args["extra_flags"]
            ]

            self.run_name = f"{run} {self.name}"
            print(f"Running: {self.run_name}:\n")

            add_to_cmd = lambda x: cmd.append(f"'{x}'")
            if run == "upload":
                add_to_cmd(s5cmd_cmds["old_upload"])
                add_to_cmd(s5cmd_cmds["new_upload"])
            elif run == "download":
                add_to_cmd(s5cmd_cmds["old_download"])
                add_to_cmd(s5cmd_cmds["new_download"])
            elif run == "remove":
                add_to_cmd(s5cmd_cmds["old_remove"])
                add_to_cmd(s5cmd_cmds["new_remove"])
                # if there is only one run without warmups, then do not prepare.
                if (
                    int(self.hyperfine_args["runs"]) > 1
                    or int(self.hyperfine_args["warmup"]) >= 1
                ):
                    cmd.append("--prepare")
                    add_to_cmd(s5cmd_cmds["prepare_old_for_remove"])
                    cmd.append("--prepare")
                    add_to_cmd(s5cmd_cmds["prepare_new_for_remove"])

            # Pass appropriate environment variables based on provider
            env_vars = {}
            
            if self.dst_path.startswith("s3://"):
                aws_profile = os.environ.get("AWS_PROFILE", "")
                if aws_profile:
                    env_vars["AWS_PROFILE"] = aws_profile
            elif self.dst_path.startswith("gs://"):
                # For GCS, you might pass GOOGLE_APPLICATION_CREDENTIALS or other GCP variables
                gcp_credentials = os.environ.get("GOOGLE_APPLICATION_CREDENTIALS", "")
                if gcp_credentials:
                    env_vars["GOOGLE_APPLICATION_CREDENTIALS"] = gcp_credentials
                
            output = run_cmd(cmd, env_vars=env_vars)
            summary = self.parse_output(output)
            if self.initialize_bench:
                init_bench_results(
                    self.cwd, self.output_file_name, self.all_scenario_details
                )
                self.initialize_bench = False

            with open(os.path.join(self.cwd, self.output_file_name), "a") as f:
                f.write(summary)

            detailed_summary = ""
            temp_dir = os.path.join(self.local_dir, "temp.md")

            with open(temp_dir, "r+") as f:
                lines = f.readlines()
                # get hyperfine markdown output and add a new column in the front as scenario name
                if lines:
                    detailed_summary = (
                        f"| {self.run_name} {lines[-1]}" f"| {self.run_name} {lines[-2]}"
                    )
                else:
                    print("No output found in temp.md file?")

            with open(os.path.join(self.cwd, "detailed_summary.md"), "a") as f:
                f.write(detailed_summary)

    def get_s5cmd_commands(self, old_s5cmd, new_s5cmd):
        result = {}

        old_upload = " ".join(
            [self.s5cmd_args, "cp", f'"*"', f"{self.dst_path}/old/"]
        )
        old_upload = f"{old_s5cmd.path} {old_upload}"
        result["old_upload"] = old_upload

        prepare_old_for_remove = f"{old_upload} | sleep 10"
        result["prepare_old_for_remove"] = prepare_old_for_remove

        new_upload = " ".join(
            [self.s5cmd_args, "cp", '"*"', f"{self.dst_path}/new/"]
        )
        new_upload = f"{new_s5cmd.path} {new_upload}"
        result["new_upload"] = new_upload

        prepare_new_for_remove = f"{new_upload} | sleep 10"
        result["prepare_new_for_remove"] = prepare_new_for_remove

        old_download = " ".join(
            [self.s5cmd_args, "cp", f'"{self.dst_path}/old/*"', "old/"]
        )
        old_download = f"{old_s5cmd.path} {old_download}"
        result["old_download"] = old_download

        new_download = " ".join(
            [self.s5cmd_args, "cp", f'"{self.dst_path}/new/*"', "new/"]
        )
        new_download = f"{new_s5cmd.path} {new_download}"
        result["new_download"] = new_download

        new_remove = " ".join(
            [self.s5cmd_args, "rm", f'"{self.dst_path}/new/*"']
        )

        new_remove = f"{new_s5cmd.path} {new_remove}"
        result["new_remove"] = new_remove

        old_remove = " ".join(
            [self.s5cmd_args, "rm", f'"{self.dst_path}/old/*"']
        )
        old_remove = f"{old_s5cmd.path} {old_remove}"
        result["old_remove"] = old_remove

        return result

    def parse_output(self, output):
        lines = output.split("\n")
        summary = ""
        for i, line in enumerate(lines):
            # get the next two lines after summary and format it as markdown table.
            if "Summary" in line:
                line1 = lines[i + 1].replace("\n", "").strip()
                line2 = lines[i + 2].replace("\n", "").strip()
                summary = f"| {self.run_name} | {line1} {line2} |\n"
        return summary


def init_bench_results(cwd, output_file_name, scenario_details):
    header = (
        "### Benchmark summary:"
        "\n| Scenario | File Size | File Count |"
        "\n|:---|:---|:---|"
        "\n"
    )
    details = "\n".join(scenario_details)
    summary = (
        f"{header}"
        f"{details}"
        "\n\n|Scenario| Summary |"
        "\n|:---|:---|"
        "\n"
    )

    with open(os.path.join(cwd, output_file_name), "w") as file:
        file.write(summary)

    detailed_summary = (
        "\n### Detailed summary: "
        "\n|Scenario| Command | Mean [s] | Min [s] | Max [s] | Relative |"
        "\n|:---|:---|---:|---:|---:|---:|"
        "\n"
    )

    with open(os.path.join(cwd, "detailed_summary.md"), "w") as file:
        file.write(detailed_summary)


def to_bytes(size):
    if size.isdigit():
        return int(size)
    unit = size[-1]
    if unit == "K":
        return int(size[:-1]) * 1024
    elif unit == "M":
        return int(size[:-1]) * (1024**2)
    elif unit == "G":
        return int(size[:-1]) * (1024**3)
    elif unit == "T":
        return int(size[:-1]) * (1024**4)
    elif unit == "P":
        return int(size[:-1]) * (1024**5)
    else:
        raise ValueError("Given size is not correct.")


def build_s5cmd_exec(old: str, new: str) -> tuple[S5cmd, S5cmd]:
    a = S5cmd(name="old", tag=old)
    b = S5cmd(name="new", tag=new)
    return a, b


def create_bench_dir(bucket, prefix, local_path, provider="s3"):
    """
    Create a benchmark directory with a unique name to specified local_path.
    If no path is specified, create temporary directory using mkdtemp.
    In both of those cases, use the same unique name to create remote path name.

    :param bucket: remote bucket to be used to create remote_path
    :param prefix: prefix to be used after specified bucket for remote_path
    :param local_path: specify a path for temporary files to be created in your local. If empty,
    use default temporary folder path of your device.
    :param provider: cloud storage provider, either "s3" or "gcs"
    :returns:
        - local_dir - created local_dir path as local_path/bench-unique_suffix
        - remote_path - created remote_path as s3://bucket/prefix/unique_suffix
                      or gs://bucket/prefix/unique_suffix for GCS

    """
    if local_path:
        basename = "bench"
        suffix = datetime.datetime.now().strftime("%y%m%d_%H%M%S")
        tmp_dir = "_".join([basename, suffix])
        if os.path.isdir(local_path):
            os.chdir(local_path)
        else:
            raise NotADirectoryError(local_path)
        os.chdir(local_path)
        os.mkdir(tmp_dir)
        os.chdir(tmp_dir)

        local_dir = os.getcwd()
        protocol = "s3" if provider == "s3" else "gs"
        remote_path = f"{protocol}://{bucket}/{prefix}/{suffix}"
    else:
        local_dir = mkdtemp(prefix=prefix)
        idx = local_dir.rfind(prefix[-1]) + 1
        protocol = "s3" if provider == "s3" else "gs"
        remote_path = f"{protocol}://{bucket}/{prefix}/{local_dir[idx:]}"
    print(f"All the local temporary files will be created at {local_dir}")
    print(f"All the remote files will be uploaded to {remote_path}")
    return local_dir, remote_path


def run_cmd(cmd, env_vars=None):
    env = os.environ.copy()
    if env_vars:
        env.update(env_vars)
    
    # Use shell=True for hyperfine commands to preserve quotes and wildcards
    use_shell = isinstance(cmd, list) and cmd[0] == "hyperfine"
    
    if use_shell:
        # Convert command list to properly quoted string
        cmd_str = " ".join(cmd)
        process = subprocess.run(cmd_str, shell=True, capture_output=True, text=True, env=env)
    else:
        process = subprocess.run(cmd, capture_output=True, text=True, env=env)
    
    print(process.stdout, end="")
    print(process.stderr, end="")
    if process.returncode != 0:
        show_cmd = cmd_str if use_shell else " ".join(cmd)
        __import__('pdb').set_trace()
        raise RuntimeError(f"Error running `{show_cmd}`: returned {process.returncode}")
    return process.stdout


def cleanup(tmp_dir, temp_result_file_dir):
    temp_summary = os.path.join(temp_result_file_dir, "detailed_summary.md")
    if os.path.isfile(temp_summary):
        os.remove(temp_summary)
    shutil.rmtree(tmp_dir)


def check_dependencies():
    """
    Checks external binary dependencies and returns bool, str tuple.
    Returns True, None if all dependencies are installed.
    Returns False, Descriptive Message if any of the dependencies is not installed.
    """
    for d in DEPENDENCIES:
        if shutil.which(d) is None:
            raise OSError(f"{d} is required but not found.")


if __name__ == "__main__":
    main()
