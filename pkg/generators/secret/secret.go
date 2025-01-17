package secret

import (
	"errors"
	"fmt"

	"golang.org/x/exp/maps"
	"gopkg.in/yaml.v3"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kusionstack.io/kusion-module-framework/pkg/module"
	v1 "kusionstack.io/kusion/pkg/apis/api.kusion.io/v1"
	"kusionstack.io/kusion/pkg/generators"
	"kusionstack.io/kusion/pkg/log"
)

type secretGenerator struct {
	project     string
	namespace   string
	secrets     map[string]v1.Secret
	secretStore *v1.SecretStore
}

type GeneratorRequest struct {
	// Project represents the Project name
	Project string
	// Namespace represents the K8s Namespace
	Namespace string
	// Workload represents the Workload configuration
	Workload v1.Accessory
	// SecretStore contains configuration to describe target secret store.
	SecretStore *v1.SecretStore
}

func NewSecretGenerator(request *GeneratorRequest) (generators.SpecGenerator, error) {
	if len(request.Project) == 0 {
		return nil, fmt.Errorf("project name must not be empty")
	}

	secretMap := make(map[string]v1.Secret)

	if request.Workload == nil {
		log.Infof("workload is missing, no secret will be generated")
		return &secretGenerator{}, nil
	}
	secrets := request.Workload["secrets"]
	if secrets != nil {
		out, err := yaml.Marshal(secrets)
		if err != nil {
			return nil, err
		}
		err = yaml.Unmarshal(out, &secretMap)
		if err != nil {
			return nil, err
		}
	}

	return &secretGenerator{
		project:     request.Project,
		secrets:     secretMap,
		namespace:   request.Namespace,
		secretStore: request.SecretStore,
	}, nil
}

func NewSecretGeneratorFunc(request *GeneratorRequest) generators.NewSpecGeneratorFunc {
	return func() (generators.SpecGenerator, error) {
		return NewSecretGenerator(request)
	}
}

func (g *secretGenerator) Generate(spec *v1.Spec) error {
	if spec.Resources == nil {
		spec.Resources = make(v1.Resources, 0)
	}

	for secretName, secretRef := range g.secrets {
		secret, err := g.generateSecret(secretName, secretRef)
		if err != nil {
			return err
		}

		resourceID := module.KubernetesResourceID(secret.TypeMeta, secret.ObjectMeta)
		err = generators.AppendToSpec(
			v1.Kubernetes,
			resourceID,
			spec,
			secret,
		)
		if err != nil {
			return err
		}
	}

	return nil
}

// generateSecret generates target secret based on secret type. Most of these secret types are just semantic wrapper
// of native Kubernetes secret types:https://kubernetes.io/docs/concepts/configuration/secret/#secret-types, and more
// detailed usage info can be found in public documentation.
func (g *secretGenerator) generateSecret(secretName string, secretRef v1.Secret) (*corev1.Secret, error) {
	switch secretRef.Type {
	case "basic":
		return g.generateBasic(secretName, secretRef)
	case "token":
		return g.generateToken(secretName, secretRef)
	case "opaque":
		return g.generateOpaque(secretName, secretRef)
	case "certificate":
		return g.generateCertificate(secretName, secretRef)
	case "external":
		return g.generateSecretWithExternalProvider(secretName, secretRef)
	default:
		return nil, fmt.Errorf("unrecognized secret type %s", secretRef.Type)
	}
}

// generateBasic generates secret used for basic authentication. The basic secret type
// is used for username / password pairs.
func (g *secretGenerator) generateBasic(secretName string, secretRef v1.Secret) (*corev1.Secret, error) {
	secret := initBasicSecret(g.namespace, secretName, corev1.SecretTypeBasicAuth, secretRef.Immutable)
	secret.Data = grabData(secretRef.Data, corev1.BasicAuthUsernameKey, corev1.BasicAuthPasswordKey)

	for _, key := range []string{corev1.BasicAuthUsernameKey, corev1.BasicAuthPasswordKey} {
		if len(secret.Data[key]) == 0 {
			v := GenerateRandomString(54)
			secret.Data[key] = []byte(v)
		}
	}

	return secret, nil
}

// generateToken generates secret used for password. Token secrets are useful for generating
// a password or secure string used for passwords when the user is already known or not required.
func (g *secretGenerator) generateToken(secretName string, secretRef v1.Secret) (*corev1.Secret, error) {
	secret := initBasicSecret(g.namespace, secretName, corev1.SecretTypeOpaque, secretRef.Immutable)
	secret.Data = grabData(secretRef.Data, "token")

	if len(secret.Data["token"]) == 0 {
		v := GenerateRandomString(54)
		secret.Data["token"] = []byte(v)
	}

	return secret, nil
}

// generateOpaque generates secret used for arbitrary user-defined data.
func (g *secretGenerator) generateOpaque(secretName string, secretRef v1.Secret) (*corev1.Secret, error) {
	secret := initBasicSecret(g.namespace, secretName, corev1.SecretTypeOpaque, secretRef.Immutable)
	secret.Data = grabData(secretRef.Data, maps.Keys(secretRef.Data)...)
	return secret, nil
}

// generateCertificate generates secret used for storing a certificate and its associated key.
// One common use for TLS Secrets is to configure encryption in transit for an Ingress, but
// you can also use it with other resources or directly in your v1.
func (g *secretGenerator) generateCertificate(secretName string, secretRef v1.Secret) (*corev1.Secret, error) {
	secret := initBasicSecret(g.namespace, secretName, corev1.SecretTypeTLS, secretRef.Immutable)
	secret.Data = grabData(secretRef.Data, corev1.TLSCertKey, corev1.TLSPrivateKeyKey)
	return secret, nil
}

// generateSecretWithExternalProvider retrieves target sensitive information from external secret provider and
// generates corresponding Kubernetes Secret object.
func (g *secretGenerator) generateSecretWithExternalProvider(secretName string, secretRef v1.Secret) (*corev1.Secret, error) {
	if g.secretStore == nil {
		return nil, errors.New("secret store is missing, please add valid secret store spec in workspace")
	}

	secret := initBasicSecret(g.namespace, secretName, corev1.SecretTypeOpaque, secretRef.Immutable)
	secret.Data = make(map[string][]byte)

	for key, ref := range secretRef.Data {
		secret.Data[key] = []byte(ref)
	}

	return secret, nil
}

// grabData extracts keys mapping data from original string map.
func grabData(from map[string]string, keys ...string) map[string][]byte {
	to := map[string][]byte{}
	for _, key := range keys {
		if v, ok := from[key]; ok {
			// don't override a non-zero length value with zero length
			if len(v) > 0 || len(to[key]) == 0 {
				to[key] = []byte(v)
			}
		}
	}
	return to
}

func initBasicSecret(namespace, name string, secretType corev1.SecretType, immutable bool) *corev1.Secret {
	secret := &corev1.Secret{
		TypeMeta: metav1.TypeMeta{
			APIVersion: corev1.SchemeGroupVersion.String(),
			Kind:       "Secret",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Immutable: &immutable,
		Type:      secretType,
	}
	return secret
}
