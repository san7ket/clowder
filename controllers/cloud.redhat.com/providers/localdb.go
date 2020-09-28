package providers

import (
	"fmt"

	crd "cloud.redhat.com/clowder/v2/apis/cloud.redhat.com/v1alpha1"
	"cloud.redhat.com/clowder/v2/controllers/cloud.redhat.com/config"
	"cloud.redhat.com/clowder/v2/controllers/cloud.redhat.com/utils"

	apps "k8s.io/api/apps/v1"
	core "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

type localDbProvider struct {
	Provider
	Config *config.DatabaseConfig
}

func (db *localDbProvider) Configure(c *config.AppConfig) {
	c.Database = db.Config
}

func NewLocalDBProvider(p *Provider) (DatabaseProvider, error) {
	return &localDbProvider{Provider: *p}, nil
}

// CreateDatabase ensures a database is created for the given app.  The
// namespaced name passed in must be the actual name of the db resources
func (db *localDbProvider) CreateDatabase(app *crd.ClowdApp) error {
	nn := types.NamespacedName{
		Name:      fmt.Sprintf("%v-db", app.Name),
		Namespace: app.Namespace,
	}

	dd := apps.Deployment{}
	exists, err := utils.UpdateOrErr(db.Client.Get(db.Ctx, nn, &dd))

	if err != nil {
		return err
	}

	if exists {
		// DB was already created
		return fmt.Errorf("DB has already been created")
	}

	cfg := config.DatabaseConfig{
		Hostname: fmt.Sprintf("%v.%v.svc", nn.Name, nn.Namespace),
		Port:     5432,
		Username: utils.RandString(16),
		Password: utils.RandString(16),
		PgPass:   utils.RandString(16),
		Name:     app.Spec.Database.Name,
	}

	makeLocalDB(&dd, nn, app, &cfg, db.Env.Spec.Database.Image)

	if _, err = exists.Apply(db.Ctx, db.Client, &dd); err != nil {
		return err
	}

	s := core.Service{}
	update, err := utils.UpdateOrErr(db.Client.Get(db.Ctx, nn, &s))

	if err != nil {
		return err
	}

	makeLocalService(&s, nn, app)

	if _, err = update.Apply(db.Ctx, db.Client, &s); err != nil {
		return err
	}

	pvc := core.PersistentVolumeClaim{}
	update, err = utils.UpdateOrErr(db.Client.Get(db.Ctx, nn, &pvc))

	if err != nil {
		return err
	}

	makeLocalPVC(&pvc, nn, app)

	if _, err = update.Apply(db.Ctx, db.Client, &pvc); err != nil {
		return err
	}

	return nil
}

func makeLocalDB(dd *apps.Deployment, nn types.NamespacedName, pp *crd.ClowdApp, cfg *config.DatabaseConfig, image string) {
	labels := pp.GetLabels()
	labels["service"] = "db"

	pp.SetObjectMeta(dd, crd.Name(nn.Name), crd.Labels(labels))
	dd.Spec.Replicas = utils.Int32(1)
	dd.Spec.Selector = &metav1.LabelSelector{MatchLabels: labels}
	dd.Spec.Template.Spec.Volumes = []core.Volume{{
		Name: nn.Name,
		VolumeSource: core.VolumeSource{
			PersistentVolumeClaim: &core.PersistentVolumeClaimVolumeSource{
				ClaimName: nn.Name,
			},
		}},
	}
	dd.Spec.Template.ObjectMeta.Labels = labels

	dd.Spec.Template.Spec.ImagePullSecrets = []core.LocalObjectReference{{
		Name: "quay-cloudservices-pull",
	}}

	envVars := []core.EnvVar{
		{Name: "POSTGRESQL_USER", Value: cfg.Username},
		{Name: "POSTGRESQL_PASSWORD", Value: cfg.Password},
		{Name: "PGPASSWORD", Value: cfg.PgPass},
		{Name: "POSTGRESQL_DATABASE", Value: pp.Spec.Database.Name},
	}
	ports := []core.ContainerPort{{
		Name:          "database",
		ContainerPort: 5432,
	}}

	probeHandler := core.Handler{
		Exec: &core.ExecAction{
			Command: []string{
				"psql",
				"-U",
				"$(POSTGRESQL_USER)",
				"-d",
				"$(POSTGRESQL_DATABASE)",
				"-c",
				"SELECT 1",
			},
		},
	}

	livenessProbe := core.Probe{
		Handler:             probeHandler,
		InitialDelaySeconds: 15,
		TimeoutSeconds:      2,
	}
	readinessProbe := core.Probe{
		Handler:             probeHandler,
		InitialDelaySeconds: 45,
		TimeoutSeconds:      2,
	}

	c := core.Container{
		Name:           nn.Name,
		Image:          image,
		Env:            envVars,
		LivenessProbe:  &livenessProbe,
		ReadinessProbe: &readinessProbe,
		Ports:          ports,
		VolumeMounts: []core.VolumeMount{{
			Name:      nn.Name,
			MountPath: "/var/lib/pgsql/data",
		}},
	}

	dd.Spec.Template.Spec.Containers = []core.Container{c}
}

func makeLocalService(s *core.Service, nn types.NamespacedName, pp *crd.ClowdApp) {
	servicePorts := []core.ServicePort{{
		Name:     "database",
		Port:     5432,
		Protocol: "TCP",
	}}

	labels := pp.GetLabels()
	labels["service"] = "db"
	pp.SetObjectMeta(s, crd.Name(nn.Name), crd.Namespace(nn.Namespace), crd.Labels(labels))
	s.Spec.Selector = labels
	s.Spec.Ports = servicePorts
}

func makeLocalPVC(pvc *core.PersistentVolumeClaim, nn types.NamespacedName, pp *crd.ClowdApp) {
	labels := pp.GetLabels()
	labels["service"] = "db"
	pp.SetObjectMeta(pvc, crd.Name(nn.Name), crd.Labels(labels))
	pvc.Spec.AccessModes = []core.PersistentVolumeAccessMode{core.ReadWriteOnce}
	pvc.Spec.Resources = core.ResourceRequirements{
		Requests: core.ResourceList{
			core.ResourceName(core.ResourceStorage): resource.MustParse("1Gi"),
		},
	}
}