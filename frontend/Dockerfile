FROM node:24-bookworm-slim AS build
WORKDIR /src
COPY package.json package-lock.json ./
RUN --mount=type=secret,id=cloud_ca if [ -f /run/secrets/cloud_ca ]; then export NODE_EXTRA_CA_CERTS=/run/secrets/cloud_ca; fi; npm ci
COPY . .
RUN npm run build

FROM nginx:1.28-alpine
COPY nginx.conf /etc/nginx/conf.d/default.conf
COPY --from=build /src/dist /usr/share/nginx/html
EXPOSE 80
