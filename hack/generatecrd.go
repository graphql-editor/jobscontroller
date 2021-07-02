package main

import (
	"bytes"
	"context"
	stdjson "encoding/json"
	"flag"
	"io/ioutil"
	"path/filepath"
	"strings"
	"time"

	"gitlab.com/Aexol/graphql_editor_microservices/pkg/generated/clientset/versioned/scheme"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/apimachinery/pkg/runtime/serializer/json"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
)

func replaceRefs(defs map[string]apiextensionsv1.JSONSchemaProps, o apiextensionsv1.JSONSchemaProps) apiextensionsv1.JSONSchemaProps {
	switch o.Type {
	case "array":
		if len(o.Items.JSONSchemas) > 0 {
			for i := range o.Items.JSONSchemas {
				o.Items.JSONSchemas[i] = replaceRefs(defs, o.Items.JSONSchemas[i])
			}
		} else if o.Items.Schema != nil {
			*o.Items.Schema = replaceRefs(defs, *o.Items.Schema)
		}
	case "", "object":
		if o.Ref != nil && *o.Ref != "" {
			return replaceRefs(defs, defs[strings.TrimPrefix(*o.Ref, "#/definitions/")])
		}
		if o.AdditionalProperties != nil && o.AdditionalProperties.Schema != nil {
			*o.AdditionalProperties.Schema = replaceRefs(defs, *o.AdditionalProperties.Schema)
		}
		for k, v := range o.Properties {
			o.Properties[k] = replaceRefs(defs, v)
		}
	}
	return o
}

// GroupName is the group name use in this package
const GroupName = ""

// SchemeGroupVersion is group version used to register these objects
var SchemeGroupVersion = schema.GroupVersion{Group: GroupName, Version: "v1"}

// KubeOpenAPI is a part of data returned by /openapi/v2
type KubeOpenAPI struct {
	Definitions map[string]apiextensionsv1.JSONSchemaProps `json:"definitions"`
}

func main() {
	var kubeconfig *string
	if home := homedir.HomeDir(); home != "" {
		kubeconfig = flag.String("kubeconfig", filepath.Join(home, ".kube", "config"), "(optional) absolute path to the kubeconfig file")
	} else {
		kubeconfig = flag.String("kubeconfig", "", "absolute path to the kubeconfig file")
	}
	flag.Parse()

	config, err := clientcmd.BuildConfigFromFlags("", *kubeconfig)
	if err != nil {
		panic(err)
	}
	config.APIPath = "/api"
	config.GroupVersion = &SchemeGroupVersion
	config.NegotiatedSerializer = serializer.WithoutConversionCodecFactory{CodecFactory: scheme.Codecs}
	cli, err := rest.RESTClientFor(config)
	if err != nil {
		panic(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*90)
	b, err := cli.Get().RequestURI("/openapi/v2").Do(ctx).Raw()
	if err != nil {
		panic(err)
	}
	cancel()
	var kubeOpenAPI KubeOpenAPI
	if err := stdjson.Unmarshal(b, &kubeOpenAPI); err != nil {
		panic(err)
	}
	// FIXME: dirty quickfix for protocol in containerport
	cp := kubeOpenAPI.Definitions["io.k8s.api.core.v1.ContainerPort"]
	prot := cp.Properties["protocol"]
	prot.Default = &apiextensionsv1.JSON{
		Raw: []byte("\"TCP\""),
	}
	cp.Properties["protocol"] = prot
	kubeOpenAPI.Definitions["io.k8s.api.core.v1.ContainerPort"] = cp
	delete(kubeOpenAPI.Definitions["io.k8s.apimachinery.pkg.apis.meta.v1.ObjectMeta"].Properties, "creationTimestamp")
	jobsTemplateSpec := replaceRefs(kubeOpenAPI.Definitions, kubeOpenAPI.Definitions["io.k8s.api.batch.v1beta1.JobTemplateSpec"])
	crd := apiextensionsv1.CustomResourceDefinition{
		TypeMeta: metav1.TypeMeta{
			Kind:       "CustomResourceDefinition",
			APIVersion: "apiextensions.k8s.io/v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: "functionjobs.jobscontroller.graphqleditor.com",
		},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: "jobscontroller.graphqleditor.com",
			Names: apiextensionsv1.CustomResourceDefinitionNames{
				Plural:   "functionjobs",
				Singular: "functionjob",
				Kind:     "FunctionJob",
				ListKind: "FunctionJobList",
			},
			Scope: apiextensionsv1.NamespaceScoped,
			Versions: []apiextensionsv1.CustomResourceDefinitionVersion{
				{
					Name:    "v1alpha1",
					Served:  true,
					Storage: true,
					Schema: &apiextensionsv1.CustomResourceValidation{
						OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{
							Type: "object",
							Properties: map[string]apiextensionsv1.JSONSchemaProps{
								"spec": {
									Type: "object",
									Properties: map[string]apiextensionsv1.JSONSchemaProps{
										"logContainerName": {
											Type:        "string",
											Description: "Optional name of container from which to capture logs",
										},
										"concatLogs": {
											Type:        "boolean",
											Description: "Optional flag enabling concatation of logs from pods with multiple containers",
										},
										"template": jobsTemplateSpec,
									},
									Required: []string{"template"},
								},
								"status": {
									Type: "object",
									Properties: map[string]apiextensionsv1.JSONSchemaProps{
										"startTime": {
											Type:        "string",
											Format:      "date-time",
											Description: "Timestamp of when job started",
										},
										"completionTime": {
											Type:        "string",
											Format:      "date-time",
											Description: "Timestamp of when job completed, only for successful jobs",
										},
										"condition": {
											Type:        "string",
											Description: "Job result, only for jobs that are finished. Either Succeeded or Failed or empty if job not yet finished",
										},
										"logs": {
											Type:        "string",
											Description: "Up to 16kB of log tail for completed jobs",
										},
									},
								},
							},
							Required: []string{"spec"},
						},
					},
				},
			},
		},
	}
	e := json.NewYAMLSerializer(json.DefaultMetaFactory, nil, nil)
	var buf bytes.Buffer
	err = e.Encode(&crd, &buf)
	if err != nil {
		panic(err)
	}
	err = ioutil.WriteFile("artifacts/crd.yaml", buf.Bytes(), 0644)
	if err != nil {
		panic(err)
	}
}
