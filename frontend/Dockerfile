# GBOS frontend image: build the React bundle, serve it with nginx.
#
# Keep NODE_VERSION in step with .github/workflows/ci.yml, so what CI proves
# buildable is what the VPS builds.
ARG NODE_VERSION=18
ARG NGINX_VERSION=1.27

FROM node:${NODE_VERSION}-alpine AS builder

WORKDIR /app

COPY package*.json ./

# ci, not install: builds from the lockfile so a rebuild months later produces
# the same bundle rather than whatever the registry has moved on to.
RUN npm ci

COPY . .

ENV REACT_APP_API_URL=/api/v1

RUN npm run build

# --- runtime ---------------------------------------------------------------

FROM nginx:${NGINX_VERSION}-alpine

RUN rm /etc/nginx/conf.d/default.conf

COPY nginx.conf /etc/nginx/conf.d/default.conf

COPY --from=builder /app/build /usr/share/nginx/html

# nginx's master process starts as root to bind port 80 and immediately drops
# its worker processes to the unprivileged `nginx` user — that is the stock
# image's behaviour and where the request handling happens. The container is
# not published to the host at all; Caddy is the only thing that reaches it.

EXPOSE 80

CMD ["nginx", "-g", "daemon off;"]
