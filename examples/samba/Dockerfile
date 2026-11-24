FROM alpine:3.24
ARG HOST_UID=501
RUN apk add --no-cache samba-server samba-common-tools \
    && adduser -D -H -u "$HOST_UID" -s /sbin/nologin sharedisk
COPY smb.conf /etc/samba/smb.conf
CMD ["smbd", "--foreground", "--no-process-group", "--debug-stdout"]
