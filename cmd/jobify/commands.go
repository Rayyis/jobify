package jobify

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	batchv1 "k8s.io/api/batch/v1"
	"k8s.io/client-go/kubernetes"
)

var Version string

const (
	CreateJobIndex = 0
	ViewJobsIndex  = 1
)

func SetupCommand() *cobra.Command {
	var cmdCreate = &cobra.Command{
		Use:   "create",
		Short: "Create a new job",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			create(getClient())
		},
	}

	var cmdList = &cobra.Command{
		Use:   "list",
		Short: "List jobs and view the details of one",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			list(getClient())
		},
	}

	var cmdView = &cobra.Command{
		Use:   "view {namespace job-name OR namespace/job-name}",
		Short: "View the details of a job",
		Args:  cobra.RangeArgs(1, 2),
		Run: func(cmd *cobra.Command, args []string) {
			clientset := getClient()
			fmt.Println("Loading job...")
			var namespace string
			var name string
			if len(args) == 2 {
				namespace = args[0]
				name = args[1]
			} else if strings.Contains(args[0], "/") {
				slashIndex := strings.Index(args[0], "/")
				namespace = args[0][0:slashIndex]
				name = args[0][slashIndex+1:]
			} else {
				fmt.Println("job details must be provided in one of two formats \"namespace job-name\" or \"namespace/job-name\"")
				os.Exit(1)
			}
			job := getJob(clientset, namespace, name)
			viewJob(clientset, job)
		},
	}
	var rootCmd = &cobra.Command{
		Use: "jobify",
		Run: func(cmd *cobra.Command, args []string) {
			jobifyRoot()
		},
		Version: Version,
	}

	rootCmd.AddCommand(cmdCreate, cmdList, cmdView)
	return rootCmd

}

func jobifyRoot() {
	faint.Println("No command given, starting in interactive mode...")
	clientset := getClient()
	i := promptOperation()
	switch i {
	case CreateJobIndex:
		create(clientset)
	case ViewJobsIndex:
		list(clientset)
	default:
		panic(fmt.Sprintf("Unexpected operation index %d", i))
	}
}

func create(clientset *kubernetes.Clientset) {
	fmt.Println("Loading deployments...")
	deploymentList := getJobifyDeployments(clientset)

	i := promptDeploymentSelection(deploymentList)

	deployment := &deploymentList.Items[i]

	err := validateDeployment(deployment)
	if err != nil {
		fmt.Printf("Invalid deployment: %s\n", err.Error())
		os.Exit(1)
	}

	defaultCommand := deployment.Annotations[DefaultCommandAnnotationKey]
	fmt.Println()
	userCommand := promptCommand(defaultCommand)

	confirmed, imageTagOverride, userCommand := promptConfirmation(deployment, "", userCommand)
	if !confirmed {
		fmt.Println("Cancelled job creation, terminating...")
		return
	}

	commandArray := setupCommandArray(deployment, userCommand)

	job := setupJob(deployment, commandArray, imageTagOverride, userCommand)

	createJob(clientset, job)
}

func list(clientset *kubernetes.Clientset) {
	fmt.Println("Loading jobs...")
	jobList := getJobifyJobs(clientset)
	sort.Slice(jobList.Items, func(i, j int) bool {
		return jobList.Items[i].CreationTimestamp.UnixNano() > jobList.Items[j].CreationTimestamp.UnixNano()
	})
	i := promptJobSelection(jobList)
	viewJob(clientset, &jobList.Items[i])
}

func viewJob(clientset *kubernetes.Clientset, job *batchv1.Job) {
	podList := getJobPods(clientset, job)
	sort.Slice(podList.Items, func(i, j int) bool {
		return podList.Items[i].CreationTimestamp.UnixNano() < podList.Items[j].CreationTimestamp.UnixNano()
	})
	printJobDetails(job, podList)
}
