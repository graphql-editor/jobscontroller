package controllers

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"
	"unsafe"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	batchinformers "k8s.io/client-go/informers/batch/v1"
	coreinformers "k8s.io/client-go/informers/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	batchlisters "k8s.io/client-go/listers/batch/v1"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	jobscontrollerv1alpha1 "github.com/graphql-editor/jobscontroller/pkg/apis/jobscontroller/v1alpha1"

	clientset "github.com/graphql-editor/jobscontroller/pkg/generated/clientset/versioned"
	jobscontrollerscheme "github.com/graphql-editor/jobscontroller/pkg/generated/clientset/versioned/scheme"
	informers "github.com/graphql-editor/jobscontroller/pkg/generated/informers/externalversions/jobscontroller/v1alpha1"
	listers "github.com/graphql-editor/jobscontroller/pkg/generated/listers/jobscontroller/v1alpha1"
)

const controllerAgentName = "jobs-controller"

const (
	// SuccessSynced is used as part of the Event 'reason' when a FunctionJob is synced
	SuccessSynced = "Synced"
	// ErrResourceExists is used as part of the Event 'reason' when a FunctionJob fails
	// to sync due to a Deployment of the same name already existing.
	ErrResourceExists = "ErrResourceExists"

	// MessageResourceExists is the message used for Events when a resource
	// fails to sync due to a Deployment already existing
	MessageResourceExists = "Resource %q already exists and is not managed by FunctionJob"
	// MessageResourceSynced is the message used for an Event fired when a FunctionJob
	// is synced successfully
	MessageResourceSynced = "FunctionJob synced successfully"
)

// JobController is a controller managing functionjob.jobcontroller.graphqleditor.com resource
type JobController struct {
	// kubeclientset is a standard kubernetes clientset
	kubeclientset kubernetes.Interface
	// sampleclientset is a clientset for our own API group
	jobscontrollerclientset clientset.Interface

	podsLister      corelisters.PodLister
	batchJobsLister batchlisters.JobLister
	batchJobsSynced cache.InformerSynced
	jobsLister      listers.FunctionJobLister
	jobsSynced      cache.InformerSynced

	// workqueue is a rate limited work queue. This is used to queue work to be
	// processed instead of performing it as soon as a change happens. This
	// means we can ensure we only process a fixed amount of resources at a
	// time, and makes it easy to ensure we are never processing the same item
	// simultaneously in two different workers.
	workqueue workqueue.RateLimitingInterface
	// recorder is an event recorder for recording Event resources to the
	// Kubernetes API.
	recorder record.EventRecorder
	context  context.Context
}

// NewController returns a new sample controller
func NewController(
	kubeclientset kubernetes.Interface,
	jobscontrollerclientset clientset.Interface,
	podsInformer coreinformers.PodInformer,
	batchJobInformer batchinformers.JobInformer,
	functionJobInformer informers.FunctionJobInformer) *JobController {
	utilruntime.Must(jobscontrollerscheme.AddToScheme(scheme.Scheme))

	klog.V(4).Info("Creating event broadcaster")
	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartStructuredLogging(0)
	eventBroadcaster.StartRecordingToSink(&typedcorev1.EventSinkImpl{Interface: kubeclientset.CoreV1().Events("")})
	recorder := eventBroadcaster.NewRecorder(scheme.Scheme, corev1.EventSource{Component: controllerAgentName})

	controller := &JobController{
		kubeclientset:           kubeclientset,
		jobscontrollerclientset: jobscontrollerclientset,
		podsLister:              podsInformer.Lister(),
		batchJobsLister:         batchJobInformer.Lister(),
		batchJobsSynced:         batchJobInformer.Informer().HasSynced,
		jobsLister:              functionJobInformer.Lister(),
		jobsSynced:              functionJobInformer.Informer().HasSynced,
		workqueue:               workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "Foos"),
		recorder:                recorder,
	}

	klog.Info("Setting up event handlers")
	// Set up an event handler for when Foo resources change
	functionJobInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: controller.enqueueFunctionJob,
		UpdateFunc: func(old, new interface{}) {
			controller.enqueueFunctionJob(new)
		},
	})
	// Ignore everything but update events as the only thing we are interested in are
	// status changes of the job. Job being a one of task
	batchJobInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		UpdateFunc: func(old, new interface{}) {
			newDepl := new.(*batchv1.Job)
			oldDepl := old.(*batchv1.Job)
			if newDepl.ResourceVersion == oldDepl.ResourceVersion {
				// Periodic resync will send update events for all known Deployments.
				// Two different versions of the same Deployment will always have different RVs.
				return
			}
			controller.handleObject(new)
		},
	})

	return controller
}

// Run will set up the event handlers for types we are interested in, as well
// as syncing informer caches and starting workers. It will block until stopCh
// is closed, at which point it will shutdown the workqueue and wait for
// workers to finish processing their current work items.
func (j *JobController) Run(ctx context.Context, threadiness int) error {
	j.context = ctx
	defer utilruntime.HandleCrash()
	defer j.workqueue.ShutDown()
	stopCh := make(chan struct{})
	go func() {
		<-ctx.Done()
		close(stopCh)
	}()

	// Start the informer factories to begin populating the informer caches
	klog.Info("Starting FunctionJob controller")

	// Wait for the caches to be synced before starting workers
	klog.Info("Waiting for informer caches to sync")
	if ok := cache.WaitForCacheSync(stopCh, j.batchJobsSynced, j.jobsSynced); !ok {
		return fmt.Errorf("failed to wait for caches to sync")
	}

	klog.Info("Starting workers")
	// Launch two workers to process FunctionJob resources
	for i := 0; i < threadiness; i++ {
		go wait.Until(j.runWorker, time.Second, stopCh)
	}

	klog.Info("Started workers")
	<-stopCh
	klog.Info("Shutting down workers")

	return nil
}

// runWorker is a long-running function that will continually call the
// processNextWorkItem function in order to read and process a message on the
// workqueue.
func (j *JobController) runWorker() {
	for j.processNextWorkItem() {
	}
}

// processNextWorkItem will read a single work item off the workqueue and
// attempt to process it, by calling the syncHandler.
func (j *JobController) processNextWorkItem() bool {
	obj, shutdown := j.workqueue.Get()

	if shutdown {
		return false
	}

	// We wrap this block in a func so we can defer c.workqueue.Done.
	err := func(obj interface{}) error {
		// We call Done here so the workqueue knows we have finished
		// processing this item. We also must remember to call Forget if we
		// do not want this work item being re-queued. For example, we do
		// not call Forget if a transient error occurs, instead the item is
		// put back on the workqueue and attempted again after a back-off
		// period.
		defer j.workqueue.Done(obj)
		var key string
		var ok bool
		// We expect strings to come off the workqueue. These are of the
		// form namespace/name. We do this as the delayed nature of the
		// workqueue means the items in the informer cache may actually be
		// more up to date that when the item was initially put onto the
		// workqueue.
		if key, ok = obj.(string); !ok {
			// As the item in the workqueue is actually invalid, we call
			// Forget here else we'd go into a loop of attempting to
			// process a work item that is invalid.
			j.workqueue.Forget(obj)
			utilruntime.HandleError(fmt.Errorf("expected string in workqueue but got %#v", obj))
			return nil
		}
		// Run the syncHandler, passing it the namespace/name string of the
		// FunctionJob resource to be synced.
		if err := j.syncHandler(key); err != nil {
			// Put the item back on the workqueue to handle any transient errors.
			j.workqueue.AddRateLimited(key)
			return fmt.Errorf("error syncing '%s': %s, requeuing", key, err.Error())
		}
		// Finally, if no error occurs we Forget this item so it does not
		// get queued again until another change happens.
		j.workqueue.Forget(obj)
		klog.Infof("Successfully synced '%s'", key)
		return nil
	}(obj)

	if err != nil {
		utilruntime.HandleError(err)
	}

	return true
}

func isFunctionJobDone(functionJob *jobscontrollerv1alpha1.FunctionJob) bool {
	return functionJob.Status.Condition == jobscontrollerv1alpha1.ConditionSuccess ||
		functionJob.Status.Condition == jobscontrollerv1alpha1.ConditionFailed
}

// syncHandler compares the actual state with the desired, and attempts to
// converge the two. It then updates the Status block of the FunctionJob resource
// with the current status of the resource.
func (j *JobController) syncHandler(key string) error {
	// Convert the namespace/name string into a distinct namespace and name
	namespace, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("invalid resource key: %s", key))
		return nil
	}

	// Get the FunctionJob resource with this namespace/name
	functionJob, err := j.jobsLister.FunctionJobs(namespace).Get(name)
	if err != nil {
		// The FunctionJob resource may no longer exist, in which case we stop
		// processing.
		if errors.IsNotFound(err) {
			utilruntime.HandleError(fmt.Errorf("functionJob '%s' in work queue no longer exists", key))
			return nil
		}

		return err
	}

	// Get the job with the same name specified as FunctionJob.name
	job, err := j.batchJobsLister.Jobs(functionJob.Namespace).Get(functionJob.Name)
	// If the resource doesn't exist, we'll create it
	if errors.IsNotFound(err) {
		// FunctionJobs are one off tasks, if it's already completed then
		// just ignore further events and do not error out on missing job.
		if isFunctionJobDone(functionJob) {
			return nil
		}
		ctx, cancel := context.WithTimeout(j.context, time.Minute*5)
		job, err = j.kubeclientset.BatchV1().Jobs(functionJob.Namespace).Create(ctx, newJob(functionJob), metav1.CreateOptions{})
		cancel()
	}

	// If an error occurs during Get/Create, we'll requeue the item so we can
	// attempt processing again later. This could have been caused by a
	// temporary network failure, or any other transient reason.
	if err != nil {
		return err
	}

	// If the Deployment is not controlled by this FunctionJob resource, we should log
	// a warning to the event recorder and return error msg.
	if !metav1.IsControlledBy(job, functionJob) {
		msg := fmt.Sprintf(MessageResourceExists, job.Name)
		j.recorder.Event(functionJob, corev1.EventTypeWarning, ErrResourceExists, msg)
		return fmt.Errorf(msg)
	}

	err = j.updateFunctionJobStatus(functionJob, job)
	if err != nil {
		return err
	}

	j.recorder.Event(functionJob, corev1.EventTypeNormal, SuccessSynced, MessageResourceSynced)
	return nil
}

func podStatusNewer(p1, p2 *corev1.Pod) bool {
	if p1.Status.StartTime == nil {
		return p2.Status.StartTime != nil
	}
	if p2.Status.StartTime == nil {
		return false
	}
	return p1.Status.StartTime.Before(p2.Status.StartTime)
}

type tailWriter struct {
	tailLogs []byte
}

func (t *tailWriter) String() string {
	return *(*string)(unsafe.Pointer(&t.tailLogs))
}

const maxTailSize = 1 << 18

func (t *tailWriter) writeFrom(r io.Reader) error {
	// only keep the 256 tailing kilobytes of log
	b := make([]byte, 1<<10)
	n, err := r.Read(b)
	for ; err == nil; n, err = r.Read(b) {
		if len(t.tailLogs)+n > maxTailSize {
			from := maxTailSize - n
			t.tailLogs = append(t.tailLogs[:0], t.tailLogs[len(t.tailLogs)-from:]...)
		}
		t.tailLogs = append(t.tailLogs, b[:n]...)
	}
	if err == io.EOF {
		err = nil
	}
	return err
}

func (j *JobController) podLogs(pod *corev1.Pod, container string, w *tailWriter) (err error) {
	podLogOpts := corev1.PodLogOptions{
		Container: container,
	}
	req := j.kubeclientset.CoreV1().Pods(pod.Namespace).GetLogs(pod.Name, &podLogOpts)
	ctx, cancel := context.WithTimeout(j.context, time.Second*15)
	defer cancel()
	podLogs, err := req.Stream(ctx)
	if err != nil {
		return
	}
	defer podLogs.Close()
	return w.writeFrom(podLogs)
}

func (j *JobController) pods(job *batchv1.Job) ([]*corev1.Pod, error) {
	labelMap, err := metav1.LabelSelectorAsMap(job.Spec.Selector)
	if err != nil {
		return nil, err
	}
	sel := labels.SelectorFromSet(labelMap)
	pods, err := j.podsLister.List(sel)
	if err == nil && len(pods) < 1 {
		err = fmt.Errorf("found %d matching %s, expected atleast 1", len(pods), sel.String())
	}
	return pods, err
}

func containerStatus(statuses []corev1.ContainerStatus, name string) *corev1.ContainerStatus {
	for _, c := range statuses {
		if c.Name == name {
			return &c
		}
	}
	return nil
}

func wasContainerRan(status *corev1.ContainerStatus) bool {
	return status != nil && (status.State.Running != nil || status.State.Terminated != nil)
}

func (j *JobController) fetchLogs(functionJob *jobscontrollerv1alpha1.FunctionJob, job *batchv1.Job) (logs string, err error) {
	pods, err := j.pods(job)
	if err != nil {
		return
	}
	pod := pods[0]
	for _, p := range pods[1:] {
		if pod.Status.Phase != corev1.PodSucceeded && p.Status.Phase == corev1.PodSucceeded {
			pod = p
		} else if podStatusNewer(pod, p) {
			pod = p
		}
	}
	w := tailWriter{
		tailLogs: make([]byte, 0, 1<<10),
	}
	if functionJob.Spec.ConcatLogs {
		containers := append(pod.Spec.InitContainers, pod.Spec.Containers...)
		containerStatuses := append(pod.Status.InitContainerStatuses, pod.Status.ContainerStatuses...)
		for i := 0; i < len(containers) && err == nil; i++ {
			name := containers[i].Name
			status := containerStatus(containerStatuses, name)
			if wasContainerRan(status) {
				err = j.podLogs(pod, name, &w)
			} else if status != nil && status.State.Waiting != nil {
				err = w.writeFrom(strings.NewReader(fmt.Sprintf("container %s was never ran, reason: %s\n", name, status.State.Waiting.Message)))
			}
		}
	} else {
		err = j.podLogs(pod, functionJob.Spec.LogContainerName, &w)
	}
	if err == nil {
		logs = w.String()
	}
	return
}

func (j *JobController) deleteJob(job *batchv1.Job) error {
	ctx, cancel := context.WithTimeout(j.context, time.Minute*5)
	defer cancel()
	var pods []*corev1.Pod
	pods, err := j.pods(job)
	if err != nil {
		return err
	}
	for _, pod := range pods {
		if err = j.kubeclientset.CoreV1().Pods(pod.Namespace).Delete(ctx, pod.Name, metav1.DeleteOptions{}); err != nil {
			return err
		}
	}
	return j.kubeclientset.BatchV1().Jobs(job.Namespace).Delete(ctx, job.Name, metav1.DeleteOptions{})
}

func (j *JobController) updateFunctionJobStatus(functionJob *jobscontrollerv1alpha1.FunctionJob, job *batchv1.Job) error {
	functionJobCopy := functionJob.DeepCopy()
	for _, cond := range job.Status.Conditions {
		if cond.Status == corev1.ConditionTrue {
			switch cond.Type {
			case batchv1.JobComplete:
				functionJobCopy.Status.Condition = jobscontrollerv1alpha1.ConditionSuccess
			case batchv1.JobFailed:
				functionJobCopy.Status.Condition = jobscontrollerv1alpha1.ConditionFailed
			}
		}
	}
	if job.Status.StartTime != nil {
		functionJob.Status.StartTime = job.Status.StartTime.DeepCopy()
	}
	if job.Status.CompletionTime != nil {
		functionJob.Status.CompletionTime = job.Status.CompletionTime.DeepCopy()
	}
	var logs string
	if isFunctionJobDone(functionJob) {
		var err error
		logs, err = j.fetchLogs(functionJob, job)
		if err == nil {
			err = j.deleteJob(job)
		}
		if err != nil {
			return err
		}
		if functionJob.Status.StartTime == nil || functionJob.Status.StartTime.IsZero() {
			functionJobCopy.Status.StartTime = job.CreationTimestamp.DeepCopy()
		}
	}
	if logs != "" {
		functionJobCopy.Status.Logs = logs
	}
	// If the CustomResourceSubresources feature gate is not enabled,
	// we must use Update instead of UpdateStatus to update the Status block of the FunctionJob resource.
	// UpdateStatus will not allow changes to the Spec of the resource,
	// which is ideal for ensuring nothing other than resource status has been updated.
	_, err := j.jobscontrollerclientset.JobscontrollerV1alpha1().FunctionJobs(functionJob.Namespace).Update(context.TODO(), functionJobCopy, metav1.UpdateOptions{})
	return err
}

// enqueueFunctionJob takes a FunctionJob resource and converts it into a namespace/name
// string which is then put onto the work queue. This method should *not* be
// passed resources of any type other than FunctionJob.
func (j *JobController) enqueueFunctionJob(obj interface{}) {
	var key string
	var err error
	if key, err = cache.MetaNamespaceKeyFunc(obj); err != nil {
		utilruntime.HandleError(err)
		return
	}
	j.workqueue.Add(key)
}

// handleObject will take any resource implementing metav1.Object and attempt
// to find the FunctionJob resource that 'owns' it. It does this by looking at the
// objects metadata.ownerReferences field for an appropriate OwnerReference.
// It then enqueues that FunctionJob resource to be processed. If the object does not
// have an appropriate OwnerReference, it will simply be skipped.
func (j *JobController) handleObject(obj interface{}) {
	var object metav1.Object
	var ok bool
	if object, ok = obj.(metav1.Object); !ok {
		tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("error decoding object, invalid type"))
			return
		}
		object, ok = tombstone.Obj.(metav1.Object)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("error decoding object tombstone, invalid type"))
			return
		}
		klog.V(4).Infof("Recovered deleted object '%s' from tombstone", object.GetName())
	}
	klog.V(4).Infof("Processing object: %s", object.GetName())
	if ownerRef := metav1.GetControllerOf(object); ownerRef != nil {
		// If this object is not owned by a FunctionJob, we should not do anything more
		// with it.
		if ownerRef.Kind != "FunctionJob" {
			return
		}

		functionJob, err := j.jobsLister.FunctionJobs(object.GetNamespace()).Get(ownerRef.Name)
		if err != nil {
			klog.V(4).Infof("ignoring orphaned object '%s' of functionJob '%s'", object.GetSelfLink(), ownerRef.Name)
			return
		}

		j.enqueueFunctionJob(functionJob)
		return
	}
}

// newJob creates a new Job for a FunctionJob resource. It also sets
// the appropriate OwnerReferences on the resource so handleObject can discover
// the FunctionJob resource that 'owns' it.
func newJob(functionJob *jobscontrollerv1alpha1.FunctionJob) *batchv1.Job {
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      functionJob.Name,
			Namespace: functionJob.Namespace,
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(functionJob, jobscontrollerv1alpha1.SchemeGroupVersion.WithKind("FunctionJob")),
			},
		},
		Spec: functionJob.Spec.Template.Spec,
	}
}
