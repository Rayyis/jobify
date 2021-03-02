package jobify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/fatih/color"
	appv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
)

const (
	CommandTemplateAnnotationKey  = "jobify/command-array-template"
	PrimaryContainerAnnotationKey = "jobify/primary-container"
	DefaultCommandAnnotationKey   = "jobify/default-command"
	SourceAliasAnnotationKey      = "jobify/source-alias"
	UserCommandAnnotationKey      = "jobify/user-command"
	SourceDeploymentAnnotationKey = "jobify/source-deployment"
	DeploymentAliasAnnotationKey  = "jobify/deployment-alias"
	LogsURLTemplateAnnotationKey  = "jobify/log-url-template"
)

func getClient() *kubernetes.Clientset {
	kubeconfig := filepath.Join(homedir.HomeDir(), ".kube", "config")

	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		fmt.Printf("Error creating Kubernetes config object: %s\n", err.Error())
		os.Exit(1)
	}

	c, err := kubernetes.NewForConfig(config)

	if err != nil {
		fmt.Printf("Error creating a Kubernetes client: %s\n", err.Error())
		os.Exit(1)
	}

	return c
}

func getJob(clientset *kubernetes.Clientset, namespace, name string) *batchv1.Job {
	job, err := clientset.BatchV1().Jobs(namespace).Get(context.TODO(), name, metav1.GetOptions{})
	if err != nil {
		fmt.Printf("Error getting job: %s\n", err.Error())
		os.Exit(1)
	}
	return job
}

func getActiveContainerStateString(containerState corev1.ContainerState) string {
	if containerState.Running != nil {
		return fmt.Sprintf("Running, Started at: %s", containerState.Running.StartedAt.String())
	} else if containerState.Waiting != nil {
		return fmt.Sprintf("Waiting, Reason: %s, Message: %s", containerState.Waiting.Reason, containerState.Waiting.Message)
	} else if containerState.Terminated != nil {
		return fmt.Sprintf("Finished, Exit code: %d, Finished at: %s, Reason: %s", containerState.Terminated.ExitCode, containerState.Terminated.FinishedAt.String(), containerState.Terminated.Reason)
	} else {
		fmt.Println("unrecognized container state!")
		return ""
	}

}

func getPodLogs(clientset *kubernetes.Clientset, pod *corev1.Pod, containerName string) string {
	tailLines := int64(10)
	req := clientset.CoreV1().Pods(pod.Namespace).GetLogs(pod.Name, &corev1.PodLogOptions{
		TailLines: &tailLines,
		Container: containerName,
	})

	podLogs, err := req.Stream(context.TODO())
	if err != nil {
		fmt.Printf("Error getting logs: %s\n", err.Error())
		os.Exit(1)
	}
	defer podLogs.Close()

	buf := new(bytes.Buffer)
	_, err = io.Copy(buf, podLogs)
	if err != nil {
		fmt.Printf("Error reading logs: %s\n", err.Error())
		os.Exit(1)
	}
	str := buf.String()
	return str
}

func getJobPods(clientset *kubernetes.Clientset, job *batchv1.Job) *corev1.PodList {
	podList, err := clientset.CoreV1().Pods(job.Namespace).List(context.TODO(), metav1.ListOptions{
		LabelSelector: fmt.Sprintf("job-name=%s", job.Name),
	})
	if err != nil {
		fmt.Printf("Error getting job pods: %s\n", err.Error())
		os.Exit(1)
	}
	return podList
}

func getJobifyJobs(clientset *kubernetes.Clientset) *batchv1.JobList {
	jobs, err := clientset.BatchV1().Jobs("").List(context.TODO(), metav1.ListOptions{
		LabelSelector: "jobify=true",
	})
	if err != nil {
		fmt.Printf("Error listing jobs: %s\n", err.Error())
		os.Exit(1)
	}
	return jobs
}

func setupCommandArray(deployment *appv1.Deployment, userCommand string) (commandArray []string) {
	commandTemplate := deployment.Annotations[CommandTemplateAnnotationKey]
	commandArrayString := strings.Replace(commandTemplate, "$JOBIFY_COMMAND", userCommand, -1)
	var arr []string
	_ = json.Unmarshal([]byte(commandArrayString), &arr)
	return arr
}

func validateDeployment(deployment *appv1.Deployment) error {
	_, ok := deployment.Annotations[CommandTemplateAnnotationKey]
	if !ok {
		return errors.New("Deployment doesn't have command template annotation " + CommandTemplateAnnotationKey)
	}

	containers := deployment.Spec.Template.Spec.Containers
	if len(containers) > 1 {
		primaryContainerName, ok := deployment.Annotations[PrimaryContainerAnnotationKey]
		if !ok {
			return errors.New("Deployment has multiple containers, but doesn't have primary container annotation " + PrimaryContainerAnnotationKey)
		}
		for _, c := range containers {
			if c.Name == primaryContainerName {
				return nil
			}
		}
		return errors.New("Deployment has multiple containers, and none of them matches the name in primary container annotation " + PrimaryContainerAnnotationKey)
	} else {
		return nil
	}
}

func getJobifyDeployments(clientset *kubernetes.Clientset) *appv1.DeploymentList {
	deployments, err := clientset.AppsV1().Deployments("").List(context.TODO(), metav1.ListOptions{
		LabelSelector: "jobify=true",
	})
	if err != nil {
		fmt.Printf("Error listing deployments: %s\n", err.Error())
		os.Exit(1)
	}
	return deployments
}

func setupJob(deployment *appv1.Deployment, commandArray []string, imageTagOverride string, userCommand string) *batchv1.Job {

	jobName := getDeploymentName(deployment) + "-" + randomString(5)

	jobTemplate := deployment.Spec.Template.DeepCopy()
	jobTemplate.Labels = map[string]string{}
	jobTemplate.Spec.RestartPolicy = "Never"
	shareProcessNamespace := true
	jobTemplate.Spec.ShareProcessNamespace = &shareProcessNamespace
	jobTemplate.Annotations["cluster-autoscaler.kubernetes.io/safe-to-evict"] = "false"

	primaryContainerIndex := getPrimaryContainer(deployment)

	if imageTagOverride != "" {
		oldImageName := jobTemplate.Spec.Containers[primaryContainerIndex].Image
		colonIndex := strings.Index(oldImageName, ":")
		if colonIndex == -1 {
			colonIndex = len(oldImageName)
		}
		newImageName := oldImageName[0:colonIndex] + ":" + imageTagOverride
		jobTemplate.Spec.Containers[primaryContainerIndex].Image = newImageName
	}

	for i := range jobTemplate.Spec.Containers {
		jobTemplate.Spec.Containers[i].ReadinessProbe = nil
		jobTemplate.Spec.Containers[i].LivenessProbe = nil
	}

	jobTemplate.Spec.Containers[primaryContainerIndex].Command = commandArray
	activeDeadlineSeconds := int64(24 * 60 * 60)
	backoffLimit := int32(2)

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: deployment.Namespace,
			Labels: map[string]string{
				"job-name": jobName,
				"jobify":   "true",
			},
			Annotations: map[string]string{
				SourceDeploymentAnnotationKey: fmt.Sprintf("%s", deployment.Name),
				SourceAliasAnnotationKey:      getDeploymentName(deployment),
				UserCommandAnnotationKey:      userCommand,
				PrimaryContainerAnnotationKey: jobTemplate.Spec.Containers[primaryContainerIndex].Name,
			},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:          &backoffLimit,
			ActiveDeadlineSeconds: &activeDeadlineSeconds,
			Template:              *jobTemplate,
		},
	}

	if logURLTemplate, ok := deployment.Annotations[LogsURLTemplateAnnotationKey]; ok {
		job.Annotations[LogsURLTemplateAnnotationKey] = logURLTemplate
	}

	return job
}

func createJob(clientset *kubernetes.Clientset, job *batchv1.Job) {
	fmt.Println("Creating job...")
	_, err := clientset.BatchV1().Jobs(job.Namespace).Create(context.TODO(), job, metav1.CreateOptions{})

	if err != nil {
		fmt.Printf("Error creating job: %s\n", err.Error())
		os.Exit(1)
	}

	fmt.Printf("Created job %s/%s successfully!\n", job.Namespace, job.Name)
	fmt.Println()
	color.New(color.Faint).Println("Use the following command to view the job's details:")
	color.New(color.FgCyan).Printf("jobify view %s %s\n", job.Namespace, job.Name)
}

func getPrimaryContainer(deployment *appv1.Deployment) int {
	containers := deployment.Spec.Template.Spec.Containers
	if len(containers) > 1 {
		primaryContainer := deployment.Annotations[PrimaryContainerAnnotationKey]
		for i, c := range containers {
			if c.Name == primaryContainer {
				return i
			}
		}
		panic("Primary container not found")
	} else {
		return 0
	}
}

func getPrimaryContainerImageTag(deployment *appv1.Deployment, imageTagOverride string) string {
	if imageTagOverride != "" {
		return imageTagOverride
	}
	containerIndex := getPrimaryContainer(deployment)
	imageName := deployment.Spec.Template.Spec.Containers[containerIndex].Image
	if strings.Contains(imageName, ":") {
		index := strings.Index(imageName, ":")
		return imageName[index+1:]
	} else {
		return ""
	}
}

func getDeploymentName(deployment *appv1.Deployment) string {
	if val, ok := deployment.Annotations[DeploymentAliasAnnotationKey]; ok {
		return val
	}
	return deployment.Name
}

func checkJobCondition(job *batchv1.Job) (successful, failed bool) {
	for _, c := range job.Status.Conditions {
		if c.Type == batchv1.JobComplete && c.Status == corev1.ConditionTrue {
			return true, false
		} else if c.Type == batchv1.JobFailed && c.Status == corev1.ConditionTrue {
			return false, true
		}
	}
	return false, false
}
