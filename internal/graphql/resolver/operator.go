package resolver

import (
	"context"
	"sync"

	operatorClient "github.com/layer5io/meshery-operator/pkg/client"
	"github.com/layer5io/meshery/internal/graphql/model"
	"github.com/layer5io/meshery/models"
	"github.com/layer5io/meshkit/errors"
	"github.com/layer5io/meshkit/utils/broadcast"
	mesherykube "github.com/layer5io/meshkit/utils/kubernetes"
)

func (r *Resolver) changeOperatorStatus(ctx context.Context, provider models.Provider, status model.Status, ctxID string) (model.Status, error) {
	delete := true

	// Tell operator status subscription that operation is starting
	r.Broadcast.Submit(broadcast.BroadcastMessage{
		Source: broadcast.OperatorSyncChannel,
		Data:   true,
		Type:   "health",
	})

	if status == model.StatusEnabled {
		r.Log.Info("Installing Operator")
		delete = false
	}

	var kubeclient *mesherykube.Client
	var k8scontext models.K8sContext
	var err error
	if ctxID != "" {
		allContexts, ok := ctx.Value(models.AllKubeClusterKey).([]models.K8sContext)
		if !ok || len(allContexts) == 0 {
			r.Log.Error(ErrNilClient)
			return model.StatusUnknown, ErrNilClient
		}
		for _, ctx := range allContexts {
			if ctx.ID == ctxID {
				k8scontext = ctx
				break
			}
		}
		kubeclient, err = k8scontext.GenerateKubeHandler()
		if err != nil {
			return model.StatusUnknown, ErrMesheryClient(err)
		}
	} else {
		k8scontexts, ok := ctx.Value(models.KubeClustersKey).([]models.K8sContext)
		if !ok || len(k8scontexts) == 0 {
			return model.StatusUnknown, ErrMesheryClient(nil)
		}
		k8scontext = k8scontexts[0]
		kubeclient, err = k8scontext.GenerateKubeHandler()
		if err != nil {
			return model.StatusUnknown, ErrMesheryClient(err)
		}
	}
	if kubeclient.KubeClient == nil {
		r.Log.Error(ErrNilClient)
		r.Broadcast.Submit(broadcast.BroadcastMessage{
			Source: broadcast.OperatorSyncChannel,
			Data:   ErrNilClient,
			Type:   "error",
		})
		return model.StatusUnknown, ErrNilClient
	}

	go func(del bool, kubeclient *mesherykube.Client) {
		err := model.Initialize(kubeclient, del, r.Config.AdapterTracker)
		if err != nil {
			r.Log.Error(err)
			r.Broadcast.Submit(broadcast.BroadcastMessage{
				Source: broadcast.OperatorSyncChannel,
				Data:   err,
				Type:   "error",
			})
			return
		}
		r.Log.Info("Operator operation executed")

		if !del {
			_, err := r.resyncCluster(context.TODO(), provider, &model.ReSyncActions{
				ReSync:    "false",
				ClearDb:   "true",
				HardReset: "false",
			}, ctxID)
			if err != nil {
				r.Log.Error(err)
				r.Broadcast.Submit(broadcast.BroadcastMessage{
					Source: broadcast.OperatorSyncChannel,
					Data:   false,
					Type:   "health",
				})
				return
			}

			endpoint, err := model.SubscribeToBroker(provider, kubeclient, r.brokerChannel, r.BrokerConn, connectionTrackerSingleton)
			r.Log.Debug("Endpoint: ", endpoint)
			if err != nil {
				r.Log.Error(err)
				r.Broadcast.Submit(broadcast.BroadcastMessage{
					Source: broadcast.OperatorSyncChannel,
					Data:   false,
					Type:   "health",
				})
				return
			}
			connectionTrackerSingleton.Set(k8scontext.ID, endpoint)
			r.Log.Info("Connected to broker at:", endpoint)
			connectionTrackerSingleton.Log(r.Log)
		}

		r.Log.Info("Meshsync operation executed")

		// r.operatorChannel <- &model.OperatorStatus{
		// 	Status: status,
		// }

		r.Broadcast.Submit(broadcast.BroadcastMessage{
			Source: broadcast.OperatorSyncChannel,
			Data:   false,
			Type:   "health",
		})
	}(delete, kubeclient)

	return model.StatusProcessing, nil
}

func (r *Resolver) getOperatorStatus(ctx context.Context, provider models.Provider, ctxID string) (*model.OperatorStatus, error) {
	status := model.StatusUnknown
	version := string(model.StatusUnknown)

	var kubeclient *mesherykube.Client
	var err error
	if ctxID != "" {
		k8scontexts, ok := ctx.Value(models.AllKubeClusterKey).([]models.K8sContext)
		if !ok || len(k8scontexts) == 0 {
			return nil, ErrMesheryClient(nil)
		}
		for _, ctx := range k8scontexts {
			if ctx.ID == ctxID {
				kubeclient, err = ctx.GenerateKubeHandler()
				if err != nil {
					return nil, ErrMesheryClient(err)
				}
				break
			}
		}
	} else {
		k8scontexts, ok := ctx.Value(models.KubeClustersKey).([]models.K8sContext)
		if !ok || len(k8scontexts) == 0 {
			return nil, ErrMesheryClient(nil)
		}
		kubeclient, err = k8scontexts[0].GenerateKubeHandler()
		if err != nil {
			return nil, ErrMesheryClient(err)
		}
	}
	if kubeclient == nil {
		return nil, ErrMesheryClient(nil)
	}
	name, version, err := model.GetOperator(kubeclient)
	if err != nil {
		r.Log.Error(err)
		return &model.OperatorStatus{
			Status: status,
			Error: &model.Error{
				Code:        "",
				Description: err.Error(),
			},
		}, nil
	}
	if name == "" {
		status = model.StatusDisabled
	} else {
		status = model.StatusEnabled
	}

	controllers, err := model.GetControllersInfo(kubeclient, r.BrokerConn)
	if err != nil {
		r.Log.Error(err)
		return &model.OperatorStatus{
			Status: status,
			Error: &model.Error{
				Code:        "",
				Description: err.Error(),
			},
		}, nil
	}

	return &model.OperatorStatus{
		Status:      status,
		Version:     version,
		Controllers: controllers,
	}, nil
}

func (r *Resolver) getMeshsyncStatus(ctx context.Context, provider models.Provider, k8scontextID string) (*model.OperatorControllerStatus, error) {
	var kubeclient *mesherykube.Client
	var err error
	if k8scontextID != "" {
		k8scontexts, ok := ctx.Value(models.AllKubeClusterKey).([]models.K8sContext)
		if !ok || len(k8scontexts) == 0 {
			return nil, ErrMesheryClient(nil)
		}
		for _, ctx := range k8scontexts {
			if ctx.ID == k8scontextID {
				kubeclient, err = ctx.GenerateKubeHandler()
				if err != nil {
					return nil, ErrMesheryClient(err)
				}
				break
			}
		}
	} else {
		k8scontexts, ok := ctx.Value(models.KubeClustersKey).([]models.K8sContext)
		if !ok || len(k8scontexts) == 0 {
			return nil, ErrMesheryClient(nil)
		}
		kubeclient, err = k8scontexts[0].GenerateKubeHandler()
		if err != nil {
			return nil, ErrMesheryClient(err)
		}
	}
	if kubeclient == nil {
		return nil, ErrMesheryClient(nil)
	}
	mesheryclient, err := operatorClient.New(&kubeclient.RestConfig)
	if err != nil {
		return nil, err
	}

	status, err := model.GetMeshSyncInfo(mesheryclient, kubeclient)
	if err != nil {
		return &status, err
	}
	return &status, nil
}

func (r *Resolver) getNatsStatus(ctx context.Context, provider models.Provider, k8scontextID string) (*model.OperatorControllerStatus, error) {
	var kubeclient *mesherykube.Client
	var err error
	if k8scontextID != "" {
		k8scontexts, ok := ctx.Value(models.AllKubeClusterKey).([]models.K8sContext)
		if !ok || len(k8scontexts) == 0 {
			return nil, ErrMesheryClient(nil)
		}
		for _, ctx := range k8scontexts {
			if ctx.ID == k8scontextID {
				kubeclient, err = ctx.GenerateKubeHandler()
				if err != nil {
					return nil, ErrMesheryClient(err)
				}
				break
			}
		}
	} else {
		k8scontexts, ok := ctx.Value(models.KubeClustersKey).([]models.K8sContext)
		if !ok || len(k8scontexts) == 0 {
			return nil, ErrMesheryClient(nil)
		}
		kubeclient, err = k8scontexts[0].GenerateKubeHandler()
		if err != nil {
			return nil, ErrMesheryClient(err)
		}
	}
	if kubeclient == nil {
		return nil, ErrMesheryClient(nil)
	}
	mesheryclient, err := operatorClient.New(&kubeclient.RestConfig)
	if err != nil {
		return nil, err
	}

	status, err := model.GetBrokerInfo(mesheryclient, kubeclient, r.BrokerConn)
	if err != nil {
		return &status, err
	}
	return &status, nil
}

func (r *Resolver) listenToOperatorsState(ctx context.Context, provider models.Provider, k8scontextIDs []string) (<-chan *model.OperatorStatusPerK8sContext, error) {
	operatorChannel := make(chan *model.OperatorStatusPerK8sContext)

	k8sctxs, ok := ctx.Value(models.AllKubeClusterKey).([]models.K8sContext)
	if !ok || len(k8sctxs) == 0 {
		return nil, ErrNilClient
	}
	var k8sContexts []models.K8sContext
	if len(k8scontextIDs) == 1 && k8scontextIDs[0] == "all" {
		k8sContexts = k8sctxs
	} else if len(k8scontextIDs) != 0 {
		var k8sContextIDsMap = make(map[string]bool)
		for _, k8sContext := range k8scontextIDs {
			k8sContextIDsMap[k8sContext] = true
		}
		for _, k8Context := range k8sctxs {
			if k8sContextIDsMap[k8Context.ID] {
				k8sContexts = append(k8sContexts, k8Context)
			}
		}
	}
	var group sync.WaitGroup
	for _, k8scontext := range k8sContexts {
		group.Add(1)
		go func(k8scontext models.K8sContext) {
			defer group.Done()
			operatorSyncChannel := make(chan broadcast.BroadcastMessage)
			r.Broadcast.Register(operatorSyncChannel)
			r.Log.Info("Operator subscription started for ", k8scontext.Name)
			err := r.connectToBroker(ctx, provider, k8scontext.ID)
			if err != nil && err != ErrNoMeshSync {
				r.Log.Error(err)
				// The subscription should remain live to send future messages and only die when context is done
				// return
			}

			// Enforce enable operator
			status, err := r.getOperatorStatus(ctx, provider, k8scontext.ID)
			if err != nil {
				r.Log.Error(ErrOperatorSubscription(err))
				return
			}
			if status.Status != model.StatusEnabled {
				_, err = r.changeOperatorStatus(ctx, provider, model.StatusEnabled, k8scontext.ID)
				if err != nil {
					r.Log.Error(ErrOperatorSubscription(err))
					// return
				}
			}
			statusWithContext := model.OperatorStatusPerK8sContext{
				ContextID:      k8scontext.ID,
				OperatorStatus: status,
			}
			operatorChannel <- &statusWithContext
			for {
				select {
				case processing := <-operatorSyncChannel:
					if processing.Source == broadcast.OperatorSyncChannel {
						r.Log.Info("Operator sync channel called for ", k8scontext.Name)
						status, err := r.getOperatorStatus(ctx, provider, k8scontext.ID)
						if err != nil {
							r.Log.Error(ErrOperatorSubscription(err))
							r.Log.Info("Operator subscription flushed for ", k8scontext.Name)
							close(operatorChannel)
							// return
							continue
						}
						switch processing.Data.(type) {
						case bool:
							if processing.Data.(bool) {
								status.Status = model.StatusProcessing
							}
						case *errors.Error:
							status.Error = &model.Error{
								Code:        processing.Data.(*errors.Error).Code,
								Description: processing.Data.(*errors.Error).Error(),
							}
						case error:
							status.Error = &model.Error{
								Code:        "",
								Description: processing.Data.(error).Error(),
							}
						}
						statusWithContext := model.OperatorStatusPerK8sContext{
							ContextID:      k8scontext.ID,
							OperatorStatus: status,
						}
						operatorChannel <- &statusWithContext
					}
				case <-ctx.Done():
					r.Log.Info("Operator subscription flushed for ", k8scontext.Name)
					r.Broadcast.Unregister(operatorSyncChannel)
					close(operatorSyncChannel)

					return
				}
			}
		}(k8scontext)
	}
	go func() {
		group.Wait()
		close(operatorChannel)
	}()
	return operatorChannel, nil
}