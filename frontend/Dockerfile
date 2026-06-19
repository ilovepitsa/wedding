FROM nginx:alpine
ARG UPLOAD_MODE=simple
COPY nginx.conf /etc/nginx/conf.d/default.conf
COPY index.html /usr/share/nginx/html/index.html
RUN sed -i "s|<!--UPLOAD_MODE-->|<script>window.UPLOAD_MODE='${UPLOAD_MODE}';</script>|" /usr/share/nginx/html/index.html
