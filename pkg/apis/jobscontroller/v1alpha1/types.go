package v1alpha1

import (
	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// FunctionJob specifices a FunctionJob resource which represents a oneoff job on Azure Function resource
type FunctionJob struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   FunctionJobSpec   `json:"spec"`
	Status FunctionJobStatus `json:"status"`
}

// FunctionJobSpec is a specification of a FunctionJob
type FunctionJobSpec struct {
	LogContainerName string                  `json:"logContainerName,omitempty"`
	ConcatLogs       bool                    `json:"concatLogs,omitempty"`
	Template         batchv1.JobTemplateSpec `json:"template"`
}

// FunctionJobCondition represents a status of finished function job
type FunctionJobCondition string

// Succeeded or Failed enums for FunctionJob
const (
	ConditionSuccess FunctionJobCondition = "Succeeded"
	ConditionFailed  FunctionJobCondition = "Failed"
)

// FunctionJobStatus is a status of a FunctionJob
type FunctionJobStatus struct {
	StartTime      *metav1.Time         `json:"startTime,omitempty"`
	CompletionTime *metav1.Time         `json:"completionTime,omitempty"`
	Condition      FunctionJobCondition `json:"condition,omitempty"`
	Logs           string               `json:"logs,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// FunctionJobList is a list of FunctionJob resources
type FunctionJobList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`

	Items []FunctionJob `json:"items"`
}
