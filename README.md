# About
A very simple controller to launch one off jobs in kubernetes cluster.

It's purpose is to start a simple job on request, collect logs from pods ran by job, and clean up the resources after the job has finished. Main motivation was that I needed a simple way to run abstract tasks in relatively isolated environment and be able to inspect results while not worrying about cleanup after the task. Something which default Job resource does not do.

# Usage

## CRD

Add crd by simply running

```
$ kubectl apply -f artifacts/crd.yaml
```

## Run against cluster

You can run it against resources in default namespace with:

```
$ go run ./cmd/jobscontroller/main.go --kubeconfig $HOME/.kube/config --namespace default
```


## Sample job:

```
apiVersion: jobscontroller.graphqleditor.com/v1alpha1
kind: FunctionJob
metadata:
  name: functionjobtest
spec:
  template:
    spec:
      template:
        spec:
          restartPolicy: Never
          containers:
          - name: busybox
            image: busybox
            args:
            - echo
            - "1"
```

And the result is:
```
$ kubectl get functionjobs.jobscontroller.graphqleditor.com functionjobtest -o yaml
apiVersion: jobscontroller.graphqleditor.com/v1alpha1
kind: FunctionJob
metadata:
  annotations:
    kubectl.kubernetes.io/last-applied-configuration: |
      {"apiVersion":"jobscontroller.graphqleditor.com/v1alpha1","kind":"FunctionJob","metadata":{"annotations":{},"name":"functionjobtest","namespace":"default"},"spec":{"template":{"spec":{"template":{"spec":{"containers":[{"args":["echo","1"],"image":"busybox","name":"busybox"}],"restartPolicy":"Never"}}}}}}
  creationTimestamp: "2021-07-02T12:45:24Z"
  generation: 4
  name: functionjobtest
  namespace: default
  resourceVersion: "171123634"
  selfLink: /apis/jobscontroller.graphqleditor.com/v1alpha1/namespaces/default/functionjobs/functionjobtest
  uid: 788d4c76-7219-4008-ac84-5dbace423107
spec:
  template:
    metadata: {}
    spec:
      template:
        metadata: {}
        spec:
          containers:
          - args:
            - echo
            - "1"
            image: busybox
            name: busybox
            resources: {}
          restartPolicy: Never
status:
  condition: Succeeded
  logs: |
    1
  startTime: "2021-07-02T12:45:24Z"
```
Or
```
$ kubectl get functionjobs.jobscontroller.graphqleditor.com functionjobtest -o jsonpath="{.status.logs}"
1
```

# Limitations
* Saves only up to 256kB of logs to keep the etcd object size resonable
* For jobs which run multiple pods only logs from from the first successful pod are stored. If all pods failed, logs from the first failed pod are stored.
