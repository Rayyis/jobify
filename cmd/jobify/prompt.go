package jobify

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/fatih/color"
	"github.com/manifoldco/promptui"
	appv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
)

var (
	cyan  *color.Color = color.New(color.FgCyan)
	faint *color.Color = color.New(color.Faint)
)

type DeploymentItem struct {
	Name      string
	Namespace string
}

type JobItem struct {
	Name      string
	Namespace string
	Source    string
	Command   string
	Status    string
	CreatedAt string
	Completed bool
	Failed    bool
	Active    bool
}

type bellSkipper struct{}

// Write implements an io.WriterCloser over os.Stderr, but it skips the terminal bell character.
func (bs *bellSkipper) Write(b []byte) (int, error) {
	const charBell = 7 // c.f. readline.CharBell
	if len(b) == 1 && b[0] == charBell {
		return 0, nil
	}
	return os.Stderr.Write(b)
}
func (bs *bellSkipper) Close() error {
	return os.Stderr.Close()
}

func promptOperation() int {
	templates := &promptui.SelectTemplates{
		Label:    "{{ . }}:",
		Active:   "> {{ . | cyan }}",
		Inactive: "  {{ . | cyan }}",
		Selected: "Selected {{ . | cyan }}",
	}

	prompt := promptui.Select{
		Label: "Select an operation to perform",
		Items: []string{
			"Create a new job",
			"View existing jobs",
		},
		Templates: templates,
		Size:      2,
		Stdout:    &bellSkipper{},
	}

	i, _, err := prompt.Run()

	if err != nil {
		if err == promptui.ErrInterrupt {
			fmt.Println("The command was interrupted ^C")
			os.Exit(1)
		}
		panic(err.Error())
	}
	return i
}

func printJobDetails(job *batchv1.Job, podList *corev1.PodList) {
	fmt.Println()
	fmt.Println("--------- Details ----------")
	completed, failed := checkJobCondition(job)
	state := "Active ⏳"
	if completed {
		state = "Completed ✅"
	} else if failed {
		state = "Failed ❌"
	}
	printAttribute("Name", job.Name)
	printAttribute("Namespace", job.Namespace)
	printAttribute("State", state)
	if job.Annotations[SourceAliasAnnotationKey] != "" && job.Annotations[SourceAliasAnnotationKey] != job.Annotations[SourceDeploymentAnnotationKey] {
		printAttribute("Deployment Alias", job.Annotations[SourceAliasAnnotationKey])
	}
	printAttribute("Deployment Name", job.Annotations[SourceDeploymentAnnotationKey])
	printAttribute("Created At", job.CreationTimestamp.String())
	printAttribute("Pod Stats", fmt.Sprintf("Active: %d, Succeeded: %d, Failed: %d", job.Status.Active, job.Status.Succeeded, job.Status.Failed))
	if len(podList.Items) > 0 {
		pods := podList.Items
		if len(podList.Items) > 2 {
			pods = pods[len(pods)-2:]
			printAttribute("Pods (last two)", "")
		} else {
			printAttribute("Pods", "")
		}
		for _, p := range pods {
			printAttributeWithIndentation(p.Name, "", 1)
			printAttributeWithIndentation("Status", string(p.Status.Phase), 2)
			printAttributeWithIndentation("Created At", p.CreationTimestamp.String(), 2)
			if p.Status.ContainerStatuses != nil && len(p.Status.ContainerStatuses) > 0 {
				printAttributeWithIndentation("Containers", "", 2)
				for _, c := range p.Status.ContainerStatuses {
					printAttributeWithIndentation(c.Name, getActiveContainerStateString(c.State), 3)
				}
			}
		}

		printAttribute("Use the following command to view logs (NOTE: this will not work once pods are garbage collected)", "")
		cyan.Printf("kubectl logs -n %s -l job-name=%s --container=%s\n", job.Namespace, job.Name, job.Annotations["jobify/primary-container"])
	} else if completed || failed {
		fmt.Println("No pods found! Pods were likely garbage collected")
	} else {
		fmt.Println("No pods found! Either they're being created, or there is a problem with the job")
	}
	if logURLTemplate, ok := job.Annotations[LogsURLTemplateAnnotationKey]; ok {
		printAttribute("Visit the link below to view logs", "")
		logsURL := logURLTemplate
		logsURL = strings.Replace(logsURL, "$JOB", job.Name, -1)
		logsURL = strings.Replace(logsURL, "$CONTAINER", job.Annotations[PrimaryContainerAnnotationKey], -1)
		cyan.Println(logsURL)

	}

}

func promptJobSelection(jobList *batchv1.JobList) int {
	jobItems := []JobItem{}

	for _, j := range jobList.Items {
		completed, failed := checkJobCondition(&j)
		jobItems = append(jobItems, JobItem{
			Name:      j.Name,
			Namespace: j.Namespace,
			Source:    j.Annotations[SourceAliasAnnotationKey],
			Command:   j.Annotations[UserCommandAnnotationKey],
			Status:    fmt.Sprintf("Active: %d, Succeeded: %d, Failed: %d", j.Status.Active, j.Status.Succeeded, j.Status.Failed),
			CreatedAt: j.GetCreationTimestamp().String(),
			Completed: completed,
			Failed:    failed,
			Active:    !completed && !failed,
		})
	}

	templates := &promptui.SelectTemplates{
		Label:    "{{ . }}:",
		Active:   "> {{ .Namespace | cyan }}/{{ .Name | cyan }}: {{ .Command }} {{ if .Completed }}✅{{ end }}{{ if .Failed }}❌{{ end }}{{ if .Active }}⏳{{ end }} {{ `Created at:` | faint }} {{ .CreatedAt | faint }}",
		Inactive: "  {{ .Namespace | cyan }}/{{ .Name | cyan }}: {{ .Command }} {{ if .Completed }}✅{{ end }}{{ if .Failed }}❌{{ end }}{{ if .Active }}⏳{{ end }} {{ `Created at:` | faint }} {{ .CreatedAt | faint }}",
		Selected: "Selected {{ .Namespace | cyan }}/{{ .Name | cyan }}",
		// 		Details: `
		// --------- Info ----------
		// {{ "Source deployment:" | faint }}	{{ .Source }}
		// {{ "Status:" | faint }}	{{ .Status }}
		// {{ "Created At:" | faint }}	{{ .CreatedAt }}`,
	}

	searcher := func(input string, index int) bool {
		item := jobItems[index]
		name := strings.Replace(strings.ToLower(item.Name), " ", "", -1) + item.Command + item.Namespace
		input = strings.Replace(strings.ToLower(input), " ", "", -1)

		return strings.Contains(name, input)
	}

	prompt := promptui.Select{
		Label:     "Select a job",
		Items:     jobItems,
		Templates: templates,
		Size:      4,
		Searcher:  searcher,
		Stdout:    &bellSkipper{},
	}

	i, _, err := prompt.Run()

	if err != nil {
		if err == promptui.ErrInterrupt {
			fmt.Println("The command was interrupted ^C")
			os.Exit(1)
		}
		panic(err.Error())
	}

	return i
}

func promptDeploymentSelection(deployments *appv1.DeploymentList) int {
	deploymentItems := []DeploymentItem{}

	for _, d := range deployments.Items {
		deploymentItems = append(deploymentItems, DeploymentItem{
			Name:      getDeploymentName(&d),
			Namespace: d.Namespace,
		})
	}

	templates := &promptui.SelectTemplates{
		Label:    "{{ . }}:",
		Active:   "> {{ .Namespace | cyan }}/{{ .Name | cyan }}",
		Inactive: "  {{ .Namespace | cyan }}/{{ .Name | cyan }}",
		Selected: "Selected {{ .Namespace | cyan }}/{{ .Name | cyan }}",
	}

	searcher := func(input string, index int) bool {
		item := deploymentItems[index]
		name := strings.Replace(strings.ToLower(item.Name), " ", "", -1) + item.Namespace
		input = strings.Replace(strings.ToLower(input), " ", "", -1)

		return strings.Contains(name, input)
	}

	prompt := promptui.Select{
		Label:     "Select a deployment",
		Items:     deploymentItems,
		Templates: templates,
		Size:      4,
		Searcher:  searcher,
		Stdout:    &bellSkipper{},
	}

	i, _, err := prompt.Run()

	if err != nil {
		if err == promptui.ErrInterrupt {
			fmt.Println("The command was interrupted ^C")
			os.Exit(1)
		}
		panic(err.Error())
	}

	return i
}

func promptConfirmation(deployment *appv1.Deployment, imageTagOverride, userCommand string) (confirmed bool, outputimageTagOverride, outputCommand string) {

	for {
		printConfirmationDetails(deployment, imageTagOverride, userCommand)
		templates := &promptui.SelectTemplates{
			Label:    "{{ . }}?",
			Active:   "> {{ . | cyan }}",
			Inactive: "  {{ . | cyan }}",
		}

		prompt := promptui.Select{
			Label: "Confirm details",
			Items: []string{
				"Confirm",
				"Edit image tag",
				"Edit command",
				"Cancel",
			},
			Templates: templates,
			Size:      4,
			Stdout:    &bellSkipper{},
		}

		i, _, err := prompt.Run()

		if err != nil {
			if err == promptui.ErrInterrupt {
				fmt.Println("The command was interrupted ^C")
				os.Exit(1)
			}
			panic(err.Error())
		}

		switch i {
		case 0:
			return true, imageTagOverride, userCommand
		case 1:
			imageTagOverride = promptImageTag(getPrimaryContainerImageTag(deployment, imageTagOverride))
		case 2:
			userCommand = promptCommand(userCommand)
		case 3:
			return false, imageTagOverride, userCommand
		}

	}
}

func printConfirmationDetails(deployment *appv1.Deployment, imageTagOverride, userCommand string) {
	fmt.Println("")
	fmt.Println("Job details:")
	printAttribute("Deployment Name", getDeploymentName(deployment))
	printAttribute("Namespace", deployment.Namespace)
	printAttribute("Image Tag", getPrimaryContainerImageTag(deployment, imageTagOverride))
	printAttribute("Command", userCommand)
}

func printAttribute(key, value string) {
	faint.Print(key + ": ")
	cyan.Println(value)
}

func printAttributeWithIndentation(key, value string, indentation int) {
	for i := 0; i < indentation; i++ {
		fmt.Print("  ")
	}
	faint.Print(key + ": ")
	cyan.Println(value)
}

func promptCommand(defaultCommand string) string {
	validate := func(input string) error {
		if input == "" {
			return errors.New("Must enter a command")
		}
		return nil
	}

	prompt := promptui.Prompt{
		Label:     "Enter the job command",
		Default:   defaultCommand,
		Validate:  validate,
		AllowEdit: true,
	}

	result, err := prompt.Run()

	if err != nil {
		if err == promptui.ErrInterrupt {
			fmt.Println("The command was interrupted ^C")
			os.Exit(1)
		}
		panic(err.Error())
	}

	return result
}

func promptImageTag(currentTag string) string {

	prompt := promptui.Prompt{
		Label:     "Enter the image tag",
		Default:   currentTag,
		AllowEdit: true,
	}

	result, err := prompt.Run()

	if err != nil {
		if err == promptui.ErrInterrupt {
			fmt.Println("The command was interrupted ^C")
			os.Exit(1)
		}
		panic(err.Error())
	}

	return result
}
