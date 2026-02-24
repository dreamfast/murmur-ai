import { createRouter, createWebHistory } from "vue-router";
import { SESSION_NICK_KEY } from "../constants.js";

const routes = [
  {
    path: "/login",
    name: "login",
    component: () => import("../views/Login.vue"),
    meta: { guest: true },
  },
  {
    path: "/",
    component: () => import("../views/Chat.vue"),
    meta: { requiresAuth: true },
    children: [
      {
        path: "",
        name: "overview",
        component: () => import("../views/Overview.vue"),
      },
      {
        path: "chat",
        name: "chat",
        component: () => import("../views/ChatContent.vue"),
      },
      {
        path: "admin",
        component: () => import("../views/Admin.vue"),
        children: [
          {
            path: "",
            redirect: { name: "admin-users" },
          },
          {
            path: "users",
            name: "admin-users",
            component: () => import("../views/admin/UsersPanel.vue"),
          },
          {
            path: "tools",
            name: "admin-tools",
            component: () => import("../views/admin/ToolsPanel.vue"),
          },
          {
            path: "tasks",
            name: "admin-tasks",
            component: () => import("../views/admin/TasksPanel.vue"),
          },
          {
            path: "channels",
            name: "admin-channels",
            component: () => import("../views/admin/ChannelsPanel.vue"),
          },
          {
            path: "stats",
            name: "admin-stats",
            component: () => import("../views/admin/StatsPanel.vue"),
          },
          {
            path: "system",
            name: "admin-system",
            component: () => import("../views/admin/SystemPanel.vue"),
          },
        ],
      },
    ],
  },
  {
    // Catch-all redirect to overview (or login if not authed).
    path: "/:pathMatch(.*)*",
    redirect: "/",
  },
];

const router = createRouter({
  history: createWebHistory(),
  routes,
});

// Navigation guard — UX convenience only, NOT a security boundary.
// The actual session is enforced server-side via HttpOnly cookies and
// HMAC-signed requests. This guard just prevents showing the chat UI
// when there's no local session hint.
router.beforeEach((to) => {
  const hasSession = !!sessionStorage.getItem(SESSION_NICK_KEY);

  if (to.meta.requiresAuth && !hasSession) {
    return { name: "login" };
  }

  if (to.meta.guest && hasSession) {
    return { name: "overview" };
  }
});

export default router;
