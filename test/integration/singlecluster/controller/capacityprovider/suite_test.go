/*
Copyright The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package capacityprovider

import (
	"context"
	"testing"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/webhooks"
	"sigs.k8s.io/kueue/test/integration/framework"
	"sigs.k8s.io/kueue/test/util"
	capacityprovidertest "sigs.k8s.io/kueue/test/util/capacityprovider"
)

var (
	cfg       *rest.Config
	k8sClient client.Client
	ctx       context.Context
	fwk       *framework.Framework
)

func TestAPIs(t *testing.T) {
	util.RunSuite(t, "CapacityProvider Test Controller Suite")
}

var _ = ginkgo.BeforeSuite(func() {
	features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.DynamicQuotaOrchestration, true)
	fwk = &framework.Framework{}
	cfg = fwk.Init()
	ctx, k8sClient = fwk.SetupClient(cfg)
	fwk.StartManager(ctx, cfg, managerSetup)
})

var _ = ginkgo.AfterSuite(func() {
	fwk.Teardown()
})

func managerSetup(_ context.Context, mgr manager.Manager) {
	failedWebhook, err := webhooks.Setup(mgr, nil)
	gomega.Expect(err).ToNot(gomega.HaveOccurred(), "webhook", failedWebhook)

	rec := capacityprovidertest.NewReconciler(mgr.GetClient())
	err = rec.SetupWithManager(mgr)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
}
