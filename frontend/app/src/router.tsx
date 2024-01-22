import { FC } from 'react';
import {
  createBrowserRouter,
  redirect,
  RouterProvider,
} from 'react-router-dom';

const routes = [
  {
    path: '/',
    lazy: async () =>
      import('./pages/root.tsx').then((res) => {
        return {
          Component: res.default,
        };
      }),
    children: [
      {
        path: '/auth',
        lazy: async () =>
          import('./pages/auth/no-auth').then((res) => {
            return {
              loader: res.loader,
            };
          }),
        children: [
          {
            path: '/auth/login',
            lazy: async () =>
              import('./pages/auth/login').then((res) => {
                return {
                  Component: res.default,
                };
              }),
          },
          {
            path: '/auth/register',
            lazy: async () =>
              import('./pages/auth/register').then((res) => {
                return {
                  Component: res.default,
                };
              }),
          },
        ],
      },
      {
        path: '/onboarding/verify-email',
        lazy: async () =>
          import('./pages/onboarding/verify-email').then((res) => {
            return {
              loader: res.loader,
              Component: res.default,
            };
          }),
      },
      {
        path: '/',
        lazy: async () =>
          import('./pages/authenticated').then((res) => {
            return {
              loader: res.loader,
              Component: res.default,
            };
          }),
        children: [
          {
            path: '/',
            lazy: async () => {
              return {
                loader: function () {
                  return redirect('/events');
                },
              };
            },
          },
          {
            path: '/onboarding/create-tenant',
            lazy: async () =>
              import('./pages/onboarding/create-tenant').then((res) => {
                return {
                  Component: res.default,
                };
              }),
          },
          {
            path: '/onboarding/invites',
            lazy: async () =>
              import('./pages/onboarding/invites').then((res) => {
                return {
                  loader: res.loader,
                  Component: res.default,
                };
              }),
          },
          {
            path: '/',
            lazy: async () =>
              import('./pages/main').then((res) => {
                return {
                  Component: res.default,
                };
              }),
            children: [
              {
                path: '/events',
                lazy: async () =>
                  import('./pages/main/events').then((res) => {
                    return {
                      Component: res.default,
                    };
                  }),
              },
              {
                path: '/events/metrics',
                lazy: async () =>
                  import('./pages/main/events/metrics').then((res) => {
                    return {
                      Component: res.default,
                    };
                  }),
              },
              {
                path: '/workflows',
                lazy: async () =>
                  import('./pages/main/workflows').then((res) => {
                    return {
                      Component: res.default,
                    };
                  }),
              },
              {
                path: '/workflows/:workflow',
                lazy: async () =>
                  import('./pages/main/workflows/$workflow').then((res) => {
                    return {
                      Component: res.default,
                      loader: res.loader,
                    };
                  }),
              },
              {
                path: '/workflow-runs',
                lazy: async () =>
                  import('./pages/main/workflow-runs').then((res) => {
                    return {
                      Component: res.default,
                    };
                  }),
              },
              {
                path: '/workflow-runs/:run',
                lazy: async () =>
                  import('./pages/main/workflow-runs/$run').then((res) => {
                    return {
                      Component: res.default,
                    };
                  }),
              },
              {
                path: '/workers',
                lazy: async () =>
                  import('./pages/main/workers').then((res) => {
                    return {
                      Component: res.default,
                    };
                  }),
              },
              {
                path: '/workers/:worker',
                lazy: async () =>
                  import('./pages/main/workers/$worker').then((res) => {
                    return {
                      Component: res.default,
                    };
                  }),
              },
              {
                path: '/tenant-settings',
                lazy: async () =>
                  import('./pages/main/tenant-settings').then((res) => {
                    return {
                      Component: res.default,
                    };
                  }),
              },
            ],
          },
        ],
      },
    ],
  },
];

const router = createBrowserRouter(routes, { basename: '/' });

const Router: FC = () => {
  return <RouterProvider router={router} />;
};

export default Router;
