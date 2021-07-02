# About
A very simple controller to launch one off jobs in kubernetes cluster.

It's purpose is to start a simple job on request, collect logs from pods ran by job, and clean up the resources after the job has finished.

# Usage

You can run it with:

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



# Limitations
* Saves only up to 256kB of logs to keep the etcd object size resonable
* For jobs which run multiple pods only logs from from the first successful pod are stored. If all pods failed, logs from the first failed pod are stored.
