# Copyright (C) 2026 Kingkor Roy Tirtho and Spotube Contributors
#
# This program is free software: you can redistribute it and/or modify
# it under the terms of the GNU Affero General Public License as published by
# the Free Software Foundation, either version 3 of the License, or
# (at your option) any later version.
#
# This program is distributed in the hope that it will be useful,
# but WITHOUT ANY WARRANTY; without even the implied warranty of
# MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
# GNU Affero General Public License for more details.
#
# You should have received a copy of the GNU Affero General Public License
# along with this program.  If not, see <https://www.gnu.org/licenses/>.

FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /spotube-jamd .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /spotube-jamd /spotube-jamd
ENV JAM_ADDR=:8787 JAM_DB=/data/jam.db
VOLUME /data
EXPOSE 8787
ENTRYPOINT ["/spotube-jamd"]
