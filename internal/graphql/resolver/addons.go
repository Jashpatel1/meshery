package resolver

import (
	"context"
	"fmt"
	"net/url"

	"github.com/layer5io/meshery/internal/graphql/model"
	"github.com/layer5io/meshery/models"
	"github.com/layer5io/meshkit/utils"
	mesherykube "github.com/layer5io/meshkit/utils/kubernetes"
	meshsyncmodel "github.com/layer5io/meshsync/pkg/model"
	corev1 "k8s.io/api/core/v1"
)

func (r *Resolver) changeAddonStatus(ctx context.Context, provider models.Provider) (model.Status, error) {
	return model.StatusProcessing, nil
}

func (r *Resolver) getAvailableAddons(ctx context.Context, provider models.Provider, selector *model.MeshType) ([]*model.AddonList, error) {
	addonlist := make([]*model.AddonList, 0)
	objects := make([]meshsyncmodel.Object, 0)

	selectors := make([]model.MeshType, 0)
	if selector == nil || *selector == model.MeshTypeAllMesh {
		for _, mesh := range model.AllMeshType {
			selectors = append(selectors, mesh)
		}
	} else {
		selectors = append(selectors, *selector)
	}

	for _, selector := range selectors {
		//subquery1 := r.DBHandler.Select("id").Where("kind = ? AND key = ? AND value = ?", meshsyncmodel.KindAnnotation, "meshery/component-type", "control-plane").Table("key_values")
		//subquery2 := r.DBHandler.Select("id").Where("id IN (?) AND kind = ? AND key = ? AND value IN (?)", subquery1, meshsyncmodel.KindAnnotation, "meshery/maintainer", selectors).Table("key_values")
		result := provider.GetGenericPersister().
			Preload("ObjectMeta", "namespace = ?", controlPlaneNamespace[selector]).
			Preload("ObjectMeta.Labels", "kind = ?", meshsyncmodel.KindLabel).
			Preload("ObjectMeta.Annotations", "kind = ?", meshsyncmodel.KindAnnotation).
			Preload("Spec").
			Preload("Status").
			Find(&objects, "kind = ?", "Service")
		if result.Error != nil {
			r.Log.Error(ErrQuery(result.Error))
			return nil, ErrQuery(result.Error)
		}

		for _, obj := range objects {
			if meshsyncmodel.IsObject(obj) && len(addonPortSelector[obj.ObjectMeta.Name]) > 0 {
				objstatus := corev1.ServiceStatus{}
				err := utils.Unmarshal(obj.Status.Attribute, &objstatus)
				if err != nil && len(obj.Status.Attribute) > 0 {
					r.Log.Error(err)
					return nil, err
				}

				objspec := corev1.ServiceSpec{}
				err = utils.Unmarshal(obj.Spec.Attribute, &objspec)
				if err != nil && len(obj.Spec.Attribute) > 0 {
					r.Log.Error(err)
					return nil, err
				}

				endpoint, err := mesherykube.GetEndpoint(context.TODO(),
					&mesherykube.ServiceOptions{
						APIServerURL: r.Config.KubeClient.RestConfig.Host,
						PortSelector: addonPortSelector[obj.ObjectMeta.Name],
					},
					&corev1.Service{
						Spec:   objspec,
						Status: objstatus,
					})
				if err != nil {
					r.Log.Error(err)
					return nil, err
				}

				if endpoint.External == nil {
					endpoint.External = endpoint.Internal
				} else {
					if !utils.TcpCheck(&utils.HostPort{
						Address: endpoint.External.Address,
						Port:    endpoint.External.Port,
					}, nil) {
						if !utils.TcpCheck(&utils.HostPort{
							Address: "host.docker.internal",
							Port:    endpoint.External.Port,
						}, nil) {
							u, _ := url.Parse(r.Config.KubeClient.RestConfig.Host)
							if utils.TcpCheck(&utils.HostPort{
								Address: u.Hostname(),
								Port:    endpoint.External.Port,
							}, nil) {
								u, _ := url.Parse(r.Config.KubeClient.RestConfig.Host)
								endpoint.External.Address = u.Hostname()
							}
						} else {
							endpoint.External.Address = "host.docker.internal"
						}
					}
				}

				addonlist = append(addonlist, &model.AddonList{
					Name:     obj.ObjectMeta.Name,
					Owner:    selector.String(),
					Endpoint: fmt.Sprintf("%s:%d", endpoint.External.Address, endpoint.External.Port),
				})
			}
		}
	}

	return addonlist, nil
}

func (r *Resolver) listenToAddonState(ctx context.Context, provider models.Provider, selector *model.MeshType) (<-chan []*model.AddonList, error) {
	if r.addonChannel == nil {
		r.addonChannel = make(chan []*model.AddonList, 0)
	}

	go func() {
		r.Log.Info("Addons subscription started")
		err := r.connectToBroker(context.TODO(), provider)
		if err != nil && err != ErrNoMeshSync {
			r.Log.Error(err)
			return
		}

		select {
		case <-r.MeshSyncChannel:
			status, err := r.getAvailableAddons(ctx, provider, selector)
			if err != nil {
				r.Log.Error(ErrAddonSubscription(err))
				return
			}
			r.addonChannel <- status
		}
	}()

	return r.addonChannel, nil
}
