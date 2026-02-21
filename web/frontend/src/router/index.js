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
        name: "admin",
        component: () => import("../views/Admin.vue"),
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
